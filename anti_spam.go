package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

type NotificationSpamConfig struct {
	MaxNotificationsPerHour         int
	MaxNotificationsPerProject      int
	NotificationCooldownPeriod      time.Duration
	DailyNotificationLimit          int
	EnableDigestMode                bool
	DigestTime                      string
	CheckIssueOpenBeforeNotify      bool
	MinIntervalBetweenNotifications time.Duration
	MaxAssignmentRequestsPerDay     int
	CommentCooldownPeriod           time.Duration
	MaxCommentsPerDay               int
	MaxGitHubCallsPerHour           int
	GitHubCallCooldown              time.Duration
}

type NotificationLogRecord struct {
	ProjectName string
	IssueURL    string
	NotifiedAt  time.Time
}

type NotificationSpamManager struct {
	config              NotificationSpamConfig
	db                  *sql.DB
	githubClient        IssueOpenChecker
	mu                  sync.RWMutex
	hourlyCount         int
	dailyCount          int
	lastHour            time.Time
	lastDay             time.Time
	projectCounts       map[string]int
	recentNotifications []NotificationLogRecord
	commentCounts       map[string]int
	githubCallCount     int
	lastGithubReset     time.Time
}

type IssueOpenChecker interface {
	CheckIssueOpen(ctx context.Context, owner, repo string, number int) (bool, error)
}

func DefaultNotificationSpamConfig() NotificationSpamConfig {
	return NotificationSpamConfig{
		MaxNotificationsPerHour:         10,
		MaxNotificationsPerProject:      2,
		NotificationCooldownPeriod:      24 * time.Hour,
		DailyNotificationLimit:          30,
		EnableDigestMode:                false,
		DigestTime:                      "09:00",
		CheckIssueOpenBeforeNotify:      true,
		MinIntervalBetweenNotifications: 5 * time.Minute,
		MaxAssignmentRequestsPerDay:     5,
		CommentCooldownPeriod:           30 * time.Minute,
		MaxCommentsPerDay:               5,
		MaxGitHubCallsPerHour:           4000,
		GitHubCallCooldown:              time.Second,
	}
}

func NewNotificationSpamManager(config NotificationSpamConfig, db *sql.DB) (*NotificationSpamManager, error) {
	manager := &NotificationSpamManager{
		config:          config,
		db:              db,
		projectCounts:   make(map[string]int),
		commentCounts:   make(map[string]int),
		lastHour:        time.Now().Truncate(time.Hour),
		lastDay:         time.Now().Truncate(24 * time.Hour),
		lastGithubReset: time.Now().Truncate(time.Hour),
	}

	if err := manager.initNotificationDB(); err != nil {
		return nil, err
	}

	if err := manager.loadRecentNotifications(); err != nil {
		log.Printf("Warning: failed to load recent notifications: %v", err)
	}

	return manager, nil
}

func (m *NotificationSpamManager) initNotificationDB() error {
	schema := `
	CREATE TABLE IF NOT EXISTS notification_log (
		id SERIAL PRIMARY KEY,
		project_name TEXT NOT NULL,
		issue_url TEXT NOT NULL,
		issue_number INTEGER,
		notified_at TIMESTAMP NOT NULL,
		hour_bucket TIMESTAMP NOT NULL,
		day_bucket DATE NOT NULL
	);

	CREATE TABLE IF NOT EXISTS comment_log (
		id SERIAL PRIMARY KEY,
		project_name TEXT NOT NULL,
		issue_url TEXT NOT NULL,
		issue_number INTEGER NOT NULL,
		comment_type TEXT NOT NULL,
		commented_at TIMESTAMP NOT NULL,
		comment_id BIGINT,
		day_bucket DATE NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_notification_log_project ON notification_log(project_name);
	CREATE INDEX IF NOT EXISTS idx_notification_log_notified_at ON notification_log(notified_at);
	CREATE INDEX IF NOT EXISTS idx_notification_log_issue_url ON notification_log(issue_url);
	CREATE INDEX IF NOT EXISTS idx_comment_log_issue_url ON comment_log(issue_url);
	CREATE INDEX IF NOT EXISTS idx_comment_log_day_bucket ON comment_log(day_bucket);
	`

	_, err := m.db.Exec(schema)
	return err
}

func (m *NotificationSpamManager) loadRecentNotifications() error {
	cutoff := time.Now().Add(-m.config.NotificationCooldownPeriod)
	query := `
	SELECT project_name, issue_url, notified_at
	FROM notification_log
	WHERE notified_at >= $1
	ORDER BY notified_at DESC
	`

	rows, err := m.db.Query(query, cutoff)
	if err != nil {
		return err
	}
	defer rows.Close()

	var records []NotificationLogRecord
	for rows.Next() {
		var r NotificationLogRecord
		if err := rows.Scan(&r.ProjectName, &r.IssueURL, &r.NotifiedAt); err != nil {
			return err
		}
		records = append(records, r)
	}

	m.mu.Lock()
	m.recentNotifications = records
	m.mu.Unlock()

	return nil
}

func (m *NotificationSpamManager) CanNotify(projectName, issueURL string) (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	currentHour := now.Truncate(time.Hour)
	currentDay := now.Truncate(24 * time.Hour)

	if currentHour.After(m.lastHour) {
		m.hourlyCount = 0
		m.projectCounts = make(map[string]int)
		m.lastHour = currentHour
	}

	if currentDay.After(m.lastDay) {
		m.dailyCount = 0
		m.lastDay = currentDay
	}

	if m.config.MaxNotificationsPerHour > 0 && m.hourlyCount >= m.config.MaxNotificationsPerHour {
		return false, fmt.Sprintf("hourly limit reached (%d/%d)", m.hourlyCount, m.config.MaxNotificationsPerHour)
	}

	if m.config.DailyNotificationLimit > 0 && m.dailyCount >= m.config.DailyNotificationLimit {
		return false, fmt.Sprintf("daily limit reached (%d/%d)", m.dailyCount, m.config.DailyNotificationLimit)
	}

	if m.config.MaxNotificationsPerProject > 0 {
		count := m.projectCounts[projectName]
		if count >= m.config.MaxNotificationsPerProject {
			return false, fmt.Sprintf("project limit reached for %s (%d/%d)", projectName, count, m.config.MaxNotificationsPerProject)
		}
	}

	for _, record := range m.recentNotifications {
		if record.IssueURL == issueURL {
			elapsed := time.Since(record.NotifiedAt)
			if elapsed < m.config.NotificationCooldownPeriod {
				remaining := m.config.NotificationCooldownPeriod - elapsed
				return false, fmt.Sprintf("cooldown period not elapsed (remaining: %v)", remaining.Round(time.Minute))
			}
		}
	}

	return true, ""
}

func (m *NotificationSpamManager) RecordNotification(projectName, issueURL string, issueNumber int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	m.hourlyCount++
	m.dailyCount++
	m.projectCounts[projectName]++

	m.recentNotifications = append([]NotificationLogRecord{{
		ProjectName: projectName,
		IssueURL:    issueURL,
		NotifiedAt:  now,
	}}, m.recentNotifications...)

	cutoff := time.Now().Add(-m.config.NotificationCooldownPeriod * 2)
	var filtered []NotificationLogRecord
	for _, r := range m.recentNotifications {
		if r.NotifiedAt.After(cutoff) {
			filtered = append(filtered, r)
		}
	}
	m.recentNotifications = filtered

	query := `
	INSERT INTO notification_log (project_name, issue_url, issue_number, notified_at, hour_bucket, day_bucket)
	VALUES ($1, $2, $3, $4, $5, $6)
	`

	_, err := m.db.Exec(query, projectName, issueURL, issueNumber, now, now.Truncate(time.Hour), now.Truncate(24*time.Hour))
	return err
}

func (m *NotificationSpamManager) CheckIssueStillOpen(ctx context.Context, owner, repo string, number int) (bool, error) {
	if m.githubClient == nil {
		return true, nil
	}
	return m.githubClient.CheckIssueOpen(ctx, owner, repo, number)
}

func (m *NotificationSpamManager) FilterNotifications(issues []Issue) []Issue {
	var filtered []Issue

	for _, issue := range issues {
		canNotify, reason := m.CanNotify(issue.Project.Name, issue.URL)
		if !canNotify {
			log.Printf("[AntiSpam] Skipping %s: %s", issue.URL, reason)
			continue
		}

		filtered = append(filtered, issue)
	}

	return filtered
}

func (m *NotificationSpamManager) CleanupOldRecords() error {
	cutoff := time.Now().AddDate(0, 0, -30)
	query := `DELETE FROM notification_log WHERE notified_at < $1`
	_, err := m.db.Exec(query, cutoff)
	return err
}

func (m *NotificationSpamManager) SetGitHubClient(client IssueOpenChecker) {
	m.githubClient = client
}

func (m *NotificationSpamManager) WasRecentlyNotified(issueURL string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, record := range m.recentNotifications {
		if record.IssueURL == issueURL {
			elapsed := time.Since(record.NotifiedAt)
			if elapsed < m.config.NotificationCooldownPeriod {
				return true
			}
		}
	}
	return false
}

func (m *NotificationSpamManager) GetDigestIssues() ([]Issue, error) {
	query := `
	SELECT issue_url, issue_title, project_name, score
	FROM notification_log nl
	LEFT JOIN issue_history ih ON nl.issue_url = ih.issue_url
	WHERE nl.notified_at >= NOW() - INTERVAL '24 hours'
	AND nl.notified_at < NOW()
	ORDER BY score DESC
	LIMIT 20
	`

	rows, err := m.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var issues []Issue
	for rows.Next() {
		var issue Issue
		var proj Project
		issue.Project = proj
		if err := rows.Scan(&issue.URL, &issue.Title, &issue.Project.Name, &issue.Score); err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}

	return issues, nil
}

func (m *NotificationSpamManager) HasCommentedOnIssue(issueURL string) (bool, error) {
	query := `SELECT 1 FROM comment_log WHERE issue_url = $1`
	var exists int
	err := m.db.QueryRow(query, issueURL).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (m *NotificationSpamManager) RecordComment(projectName, issueURL string, issueNumber int, commentType string, commentID int64) error {
	now := time.Now()
	query := `
	INSERT INTO comment_log (project_name, issue_url, issue_number, comment_type, commented_at, comment_id, day_bucket)
	VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	_, err := m.db.Exec(query, projectName, issueURL, issueNumber, commentType, now, commentID, now.Truncate(24*time.Hour))
	return err
}

func (m *NotificationSpamManager) GetCommentCount(projectKey string) (int, error) {
	today := time.Now().Truncate(24 * time.Hour)
	query := `SELECT COUNT(*) FROM comment_log WHERE project_name = $1 AND day_bucket = $2`

	var count int
	err := m.db.QueryRow(query, projectKey, today).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}

func (m *NotificationSpamManager) CanCommentOnProject(projectKey string) (bool, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count, err := m.GetCommentCount(projectKey)
	if err != nil {
		log.Printf("Warning: failed to get comment count: %v", err)
		return true, ""
	}

	if count >= 3 {
		return false, fmt.Sprintf("max comments per project per day reached (%d/3)", count)
	}

	return true, ""
}

func (m *NotificationSpamManager) WasAlreadyNotified(issueURL string) (bool, error) {
	query := `SELECT 1 FROM notification_log WHERE issue_url = $1`
	var exists int
	err := m.db.QueryRow(query, issueURL).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (m *NotificationSpamManager) CanMakeGitHubCall() (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	currentHour := now.Truncate(time.Hour)

	if !m.lastGithubReset.Equal(currentHour) {
		m.githubCallCount = 0
		m.lastGithubReset = currentHour
	}

	if m.config.MaxGitHubCallsPerHour > 0 && m.githubCallCount >= m.config.MaxGitHubCallsPerHour {
		return false, fmt.Sprintf("GitHub API rate limit reached (%d/%d)", m.githubCallCount, m.config.MaxGitHubCallsPerHour)
	}

	return true, ""
}

func (m *NotificationSpamManager) RecordGitHubCall() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.githubCallCount++
}

func (m *NotificationSpamManager) CanComment(issueURL string) (bool, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	commented, err := m.HasCommentedOnIssue(issueURL)
	if err != nil {
		log.Printf("Warning: failed to check comment history: %v", err)
	}
	if commented {
		return false, "already commented on this issue"
	}

	today := time.Now().Truncate(24 * time.Hour)
	query := `SELECT COUNT(*) FROM comment_log WHERE day_bucket = $1`
	var count int
	err = m.db.QueryRow(query, today).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("Warning: failed to get daily comment count: %v", err)
	}

	if m.config.MaxCommentsPerDay > 0 && count >= m.config.MaxCommentsPerDay {
		return false, fmt.Sprintf("daily comment limit reached (%d/%d)", count, m.config.MaxCommentsPerDay)
	}

	return true, ""
}

func (m *NotificationSpamManager) GetDailyCommentCount() (int, error) {
	today := time.Now().Truncate(24 * time.Hour)
	query := `SELECT COUNT(*) FROM comment_log WHERE day_bucket = $1`
	var count int
	err := m.db.QueryRow(query, today).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}

func (m *NotificationSpamManager) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	dailyCommentCount, _ := m.GetDailyCommentCount()

	return map[string]interface{}{
		"hourly_notifications": m.hourlyCount,
		"hourly_limit":         m.config.MaxNotificationsPerHour,
		"daily_notifications":  m.dailyCount,
		"daily_limit":          m.config.DailyNotificationLimit,
		"projects_notified":    len(m.projectCounts),
		"recent_notifications": len(m.recentNotifications),
		"daily_comments":       dailyCommentCount,
		"max_comments_per_day": m.config.MaxCommentsPerDay,
		"github_calls":         m.githubCallCount,
		"github_calls_limit":   m.config.MaxGitHubCallsPerHour,
	}
}

func (m *NotificationSpamManager) WasAssignmentRequested(issueURL string) (bool, error) {
	query := `SELECT 1 FROM assignment_requests WHERE issue_url = $1`
	var exists int
	err := m.db.QueryRow(query, issueURL).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (m *NotificationSpamManager) RecordAssignmentRequest(issueURL, projectName string, issueNumber int) error {
	now := time.Now()
	query := `
	INSERT INTO assignment_requests (issue_url, project_name, issue_number, requested_at, day_bucket)
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (issue_url) DO NOTHING
	`
	_, err := m.db.Exec(query, issueURL, projectName, issueNumber, now, now.Truncate(24*time.Hour))
	return err
}

func (m *NotificationSpamManager) GetDailyAssignmentCount() (int, error) {
	today := time.Now().Truncate(24 * time.Hour)
	query := `SELECT COUNT(*) FROM assignment_requests WHERE day_bucket = $1`
	var count int
	err := m.db.QueryRow(query, today).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}

func (m *NotificationSpamManager) CanRequestAssignment(issueURL string) (bool, string) {
	requested, err := m.WasAssignmentRequested(issueURL)
	if err != nil {
		log.Printf("Warning: failed to check assignment history: %v", err)
	}
	if requested {
		return false, "assignment already requested for this issue"
	}

	count, err := m.GetDailyAssignmentCount()
	if err != nil {
		log.Printf("Warning: failed to get daily assignment count: %v", err)
	}

	if m.config.MaxAssignmentRequestsPerDay > 0 && count >= m.config.MaxAssignmentRequestsPerDay {
		return false, fmt.Sprintf("daily assignment limit reached (%d/%d)", count, m.config.MaxAssignmentRequestsPerDay)
	}

	return true, ""
}

func (m *NotificationSpamManager) GetInteractionHistory(issueURL string) (map[string]interface{}, error) {
	history := make(map[string]interface{})

	notified, _ := m.WasAlreadyNotified(issueURL)
	history["notified"] = notified

	commented, _ := m.HasCommentedOnIssue(issueURL)
	history["commented"] = commented

	assignmentRequested, _ := m.WasAssignmentRequested(issueURL)
	history["assignment_requested"] = assignmentRequested

	return history, nil
}
