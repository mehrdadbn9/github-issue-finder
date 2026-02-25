package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v58/github"
)

type AssignmentStatus string

const (
	AssignmentPending    AssignmentStatus = "pending"
	AssignmentAsked      AssignmentStatus = "asked"
	AssignmentAssigned   AssignmentStatus = "assigned"
	AssignmentDeclined   AssignmentStatus = "declined"
	AssignmentMaxReached AssignmentStatus = "max_reached"
)

type AssignmentRequest struct {
	ID           int64
	IssueURL     string
	IssueNumber  int
	ProjectOrg   string
	ProjectName  string
	Status       AssignmentStatus
	RequestedAt  time.Time
	RespondedAt  *time.Time
	CommentID    int64
	ErrorMessage string
}

type AssignmentManager struct {
	client        *github.Client
	db            *sql.DB
	username      string
	spamManager   *AssignmentSpamManager
	mu            sync.RWMutex
	maxDailyLimit int
	enabled       bool
	autoMode      bool
}

type AssignmentSpamManager struct {
	db              *sql.DB
	dailyCount      int
	lastReset       time.Time
	commentCooldown map[string]time.Time
	mu              sync.RWMutex
}

func NewAssignmentSpamManager(db *sql.DB) (*AssignmentSpamManager, error) {
	manager := &AssignmentSpamManager{
		db:              db,
		commentCooldown: make(map[string]time.Time),
		lastReset:       time.Now().Truncate(24 * time.Hour),
	}

	if err := manager.initDB(); err != nil {
		return nil, err
	}

	if err := manager.loadDailyCount(); err != nil {
		log.Printf("Warning: failed to load daily assignment count: %v", err)
	}

	return manager, nil
}

func (m *AssignmentSpamManager) initDB() error {
	schema := `
	CREATE TABLE IF NOT EXISTS assignment_requests (
		id SERIAL PRIMARY KEY,
		issue_url TEXT UNIQUE NOT NULL,
		issue_number INTEGER NOT NULL,
		project_org TEXT NOT NULL,
		project_name TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		requested_at TIMESTAMP NOT NULL,
		responded_at TIMESTAMP,
		comment_id BIGINT,
		error_message TEXT,
		day_bucket DATE NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_assignment_requests_issue_url ON assignment_requests(issue_url);
	CREATE INDEX IF NOT EXISTS idx_assignment_requests_day_bucket ON assignment_requests(day_bucket);
	CREATE INDEX IF NOT EXISTS idx_assignment_requests_status ON assignment_requests(status);
	`

	_, err := m.db.Exec(schema)
	return err
}

func (m *AssignmentSpamManager) loadDailyCount() error {
	today := time.Now().Truncate(24 * time.Hour)
	query := `SELECT COUNT(*) FROM assignment_requests WHERE day_bucket = $1 AND status = 'asked'`

	var count int
	err := m.db.QueryRow(query, today).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	m.mu.Lock()
	m.dailyCount = count
	m.lastReset = today
	m.mu.Unlock()

	return nil
}

func (m *AssignmentSpamManager) CanRequestAssignment(projectKey string) (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	today := now.Truncate(24 * time.Hour)

	if !m.lastReset.Equal(today) {
		m.dailyCount = 0
		m.lastReset = today
	}

	if m.dailyCount >= 5 {
		return false, fmt.Sprintf("daily assignment limit reached (%d/5)", m.dailyCount)
	}

	if lastComment, exists := m.commentCooldown[projectKey]; exists {
		elapsed := time.Since(lastComment)
		if elapsed < 30*time.Minute {
			remaining := 30*time.Minute - elapsed
			return false, fmt.Sprintf("cooldown active for %s (remaining: %v)", projectKey, remaining.Round(time.Minute))
		}
	}

	return true, ""
}

func (m *AssignmentSpamManager) RecordAssignmentRequest(projectKey string, request *AssignmentRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.dailyCount++
	m.commentCooldown[projectKey] = time.Now()

	query := `
	INSERT INTO assignment_requests (issue_url, issue_number, project_org, project_name, status, requested_at, comment_id, day_bucket)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	ON CONFLICT (issue_url) DO UPDATE SET
		status = EXCLUDED.status,
		responded_at = EXCLUDED.responded_at,
		comment_id = EXCLUDED.comment_id,
		error_message = EXCLUDED.error_message
	`

	_, err := m.db.Exec(query,
		request.IssueURL,
		request.IssueNumber,
		request.ProjectOrg,
		request.ProjectName,
		request.Status,
		request.RequestedAt,
		request.CommentID,
		time.Now().Truncate(24*time.Hour),
	)

	return err
}

func (m *AssignmentSpamManager) HasAskedForAssignment(issueURL string) (bool, error) {
	query := `SELECT 1 FROM assignment_requests WHERE issue_url = $1 AND status IN ('asked', 'assigned')`
	var exists int
	err := m.db.QueryRow(query, issueURL).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (m *AssignmentSpamManager) GetDailyStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return map[string]interface{}{
		"daily_requests": m.dailyCount,
		"daily_limit":    5,
		"last_reset":     m.lastReset.Format("2006-01-02"),
	}
}

func NewAssignmentManager(client *github.Client, db *sql.DB, username string, enabled bool, autoMode bool) (*AssignmentManager, error) {
	spamManager, err := NewAssignmentSpamManager(db)
	if err != nil {
		return nil, fmt.Errorf("failed to create spam manager: %w", err)
	}

	return &AssignmentManager{
		client:        client,
		db:            db,
		username:      username,
		spamManager:   spamManager,
		maxDailyLimit: 5,
		enabled:       enabled,
		autoMode:      autoMode,
	}, nil
}

type AssignmentCandidate struct {
	Issue       *github.Issue
	ProjectOrg  string
	ProjectName string
	Labels      []string
	HasPR       bool
}

func (m *AssignmentManager) IsAssignmentCandidate(issue *github.Issue, org, repo string) (*AssignmentCandidate, bool) {
	if issue == nil || issue.Assignees != nil && len(issue.Assignees) > 0 {
		return nil, false
	}

	if issue.GetState() != "open" {
		return nil, false
	}

	labels := getLabelNames(issue.Labels)
	hasGoodFirst := false
	hasConfirmed := false

	for _, label := range labels {
		labelLower := strings.ToLower(label)
		if strings.Contains(labelLower, "good first issue") || strings.Contains(labelLower, "good-first-issue") {
			hasGoodFirst = true
		}
		if strings.Contains(labelLower, "confirmed") || strings.Contains(labelLower, "triage/accepted") ||
			strings.Contains(labelLower, "triage accepted") || strings.Contains(labelLower, "accepted") {
			hasConfirmed = true
		}
	}

	if !hasGoodFirst || !hasConfirmed {
		return nil, false
	}

	hasPR := issue.PullRequestLinks != nil

	candidate := &AssignmentCandidate{
		Issue:       issue,
		ProjectOrg:  org,
		ProjectName: repo,
		Labels:      labels,
		HasPR:       hasPR,
	}

	return candidate, true
}

func (m *AssignmentManager) CheckForLinkedPR(ctx context.Context, org, repo string, issueNumber int) (bool, error) {
	query := fmt.Sprintf("repo:%s/%s is:pr %d in:body", org, repo, issueNumber)
	result, _, err := m.client.Search.Issues(ctx, query, nil)
	if err != nil {
		return false, err
	}
	return *result.Total > 0, nil
}

func (m *AssignmentManager) AskForAssignment(ctx context.Context, candidate *AssignmentCandidate) (*AssignmentRequest, error) {
	issueURL := candidate.Issue.GetHTMLURL()

	alreadyAsked, err := m.spamManager.HasAskedForAssignment(issueURL)
	if err != nil {
		return nil, fmt.Errorf("failed to check assignment history: %w", err)
	}
	if alreadyAsked {
		return &AssignmentRequest{
			IssueURL:    issueURL,
			IssueNumber: candidate.Issue.GetNumber(),
			Status:      AssignmentDeclined,
		}, nil
	}

	hasPR := candidate.HasPR
	if !hasPR {
		hasPR, _ = m.CheckForLinkedPR(ctx, candidate.ProjectOrg, candidate.ProjectName, candidate.Issue.GetNumber())
	}

	if hasPR {
		return &AssignmentRequest{
			IssueURL:     issueURL,
			IssueNumber:  candidate.Issue.GetNumber(),
			Status:       AssignmentDeclined,
			ErrorMessage: "Issue has linked PR",
		}, nil
	}

	projectKey := fmt.Sprintf("%s/%s", candidate.ProjectOrg, candidate.ProjectName)
	canRequest, reason := m.spamManager.CanRequestAssignment(projectKey)
	if !canRequest {
		return &AssignmentRequest{
			IssueURL:     issueURL,
			IssueNumber:  candidate.Issue.GetNumber(),
			Status:       AssignmentMaxReached,
			ErrorMessage: reason,
		}, nil
	}

	if !m.autoMode {
		if !m.promptUser(candidate) {
			return &AssignmentRequest{
				IssueURL:    issueURL,
				IssueNumber: candidate.Issue.GetNumber(),
				Status:      AssignmentDeclined,
			}, nil
		}
	}

	request := &AssignmentRequest{
		IssueURL:    issueURL,
		IssueNumber: candidate.Issue.GetNumber(),
		ProjectOrg:  candidate.ProjectOrg,
		ProjectName: candidate.ProjectName,
		Status:      AssignmentPending,
		RequestedAt: time.Now(),
	}

	commentBody := fmt.Sprintf("Hi, I'd like to work on this issue. Could a maintainer please assign it to me? Thank you!")

	comment, _, err := m.client.Issues.CreateComment(ctx, candidate.ProjectOrg, candidate.ProjectName, candidate.Issue.GetNumber(), &github.IssueComment{
		Body: github.String(commentBody),
	})
	if err != nil {
		request.Status = AssignmentDeclined
		request.ErrorMessage = err.Error()
		return request, fmt.Errorf("failed to post comment: %w", err)
	}

	request.Status = AssignmentAsked
	if comment != nil {
		request.CommentID = comment.GetID()
	}

	projectKey = fmt.Sprintf("%s/%s", candidate.ProjectOrg, candidate.ProjectName)
	if err := m.spamManager.RecordAssignmentRequest(projectKey, request); err != nil {
		log.Printf("Warning: failed to record assignment request: %v", err)
	}

	log.Printf("[Assignment] Successfully requested assignment for %s#%d", candidate.ProjectName, candidate.Issue.GetNumber())
	return request, nil
}

func (m *AssignmentManager) promptUser(candidate *AssignmentCandidate) bool {
	fmt.Printf("\n%s\n", strings.Repeat("=", 60))
	fmt.Printf("NEW ASSIGNMENT OPPORTUNITY\n")
	fmt.Printf("%s\n", strings.Repeat("-", 60))
	fmt.Printf("Issue: %s\n", candidate.Issue.GetTitle())
	fmt.Printf("Project: %s/%s\n", candidate.ProjectOrg, candidate.ProjectName)
	fmt.Printf("URL: %s\n", candidate.Issue.GetHTMLURL())
	fmt.Printf("Labels: %s\n", strings.Join(candidate.Labels, ", "))
	fmt.Printf("Comments: %d | Created: %s\n", candidate.Issue.GetComments(), candidate.Issue.GetCreatedAt().Format("2006-01-02"))
	fmt.Printf("%s\n", strings.Repeat("-", 60))
	fmt.Printf("Would you like to request assignment? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	return response == "y" || response == "yes"
}

func (m *AssignmentManager) ProcessIssuesForAssignment(ctx context.Context, issues []*github.Issue, org, repo string) ([]*AssignmentRequest, error) {
	var requests []*AssignmentRequest

	for _, issue := range issues {
		candidate, isCandidate := m.IsAssignmentCandidate(issue, org, repo)
		if !isCandidate {
			continue
		}

		request, err := m.AskForAssignment(ctx, candidate)
		if err != nil {
			log.Printf("[Assignment] Error processing issue %s#%d: %v", repo, issue.GetNumber(), err)
			continue
		}

		if request != nil {
			requests = append(requests, request)
		}
	}

	return requests, nil
}

func (m *AssignmentManager) GetStats() map[string]interface{} {
	stats := m.spamManager.GetDailyStats()
	stats["enabled"] = m.enabled
	stats["auto_mode"] = m.autoMode
	return stats
}

func (m *AssignmentManager) IsEnabled() bool {
	return m.enabled
}
