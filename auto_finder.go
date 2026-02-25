// Package main contains the auto-finder system for discovering and commenting on
// GitHub issues that match quality criteria and avoid spam.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v58/github"
	"github.com/jmoiron/sqlx"
)

// AutoFinder searches for GitHub issues and automatically generates smart comments
// on high-quality issues while respecting rate limits and spam prevention rules.
type AutoFinder struct {
	config       *AutoFinderConfig        // Configuration for auto-finder behavior
	db           *sqlx.DB                 // Database connection (optional)
	githubClient *github.Client           // GitHub API client
	antiSpam     *NotificationSpamManager // Spam detection and prevention
	repoManager  *RepoManager             // Repository configuration management
	scorer       *EnhancedScorer          // Issue scoring system
	commentQueue []CommentRequest         // Queue of pending comments
	fileStorage  *FileStorage             // File-based storage when DB unavailable
	useDB        bool                     // Whether to use database for persistence
	mu           sync.Mutex               // Mutex for thread-safe operations
	smartLimiter *SmartLimiter            // Smart rate limiting
	strategy     *CommentStrategy         // Comment selection strategy
}

// AutoFinderConfig controls the behavior of the auto-finder including
// search parameters, comment limits, quality thresholds, and notification preferences.
type AutoFinderConfig struct {
	Enabled                 bool     // Enable auto-finder functionality
	AutoComment             bool     // Automatically post comments on matching issues
	AutoSearch              bool     // Automatically search for issues
	MaxCommentsPerDay       int      // Maximum comments to post in one day
	MaxCommentsPerRepo      int      // Maximum comments per repository per day
	MinScoreToComment       float64  // Minimum score threshold to post comment (0.0-1.0)
	MinHoursBetweenComments int      // Minimum hours between comments on same issue
	SearchTime              string   // Cron expression for scheduled searches
	IncludedRepos           []string // List of repos to search (empty = all enabled)
	ExcludedRepos           []string // List of repos to skip
	NotifyOnComment         bool     // Send notifications when comments are posted
	NotifyOnFind            bool     // Send notifications when issues are found
	EmailResults            bool     // Email results to configured recipients
}

// CommentRequest represents a pending comment to be posted on an issue.
type CommentRequest struct {
	IssueURL    string  // Full GitHub URL to the issue
	Repo        string  // Repository name
	IssueNumber int     // Issue number in the repository
	Comment     string  // Comment text to post
	Score       float64 // Issue quality score
	Reason      string  // Reason for posting the comment
}

// AutoFinderCommentRecord represents a historical record of a comment posted by auto-finder.
type AutoFinderCommentRecord struct {
	ID          int       // Unique record ID
	Repo        string    // Repository name
	IssueNumber int       // Issue number
	CommentedAt time.Time // When the comment was posted
	CommentText string    // The comment text
	Score       float64   // Score of the issue at time of comment
}

// ScoredIssue represents a GitHub issue with its calculated quality score and metadata.
type ScoredIssue struct {
	Issue     *github.Issue // GitHub issue object
	Project   Project       // Project information
	Score     IssueScore    // Calculated score components
	IssueData Issue         // Extracted issue data
}

// DefaultAutoFinderConfig returns a safe default configuration for the auto-finder
// with conservative limits to avoid spam and excessive API usage.
func DefaultAutoFinderConfig() *AutoFinderConfig {
	return &AutoFinderConfig{
		Enabled:                 false,
		AutoComment:             false,
		AutoSearch:              true,
		MaxCommentsPerDay:       3,
		MaxCommentsPerRepo:      1,
		MinScoreToComment:       0.75,
		MinHoursBetweenComments: 2,
		SearchTime:              "0 9 * * *",
		IncludedRepos:           []string{},
		ExcludedRepos:           []string{},
		NotifyOnComment:         true,
		NotifyOnFind:            true,
		EmailResults:            false,
	}
}

// NewAutoFinder creates and initializes a new AutoFinder with the provided configuration.
// Sets up database schema if using database, initializes file storage as fallback,
// and creates smart limiter and strategy components.
// Returns an error if initialization fails (invalid config, database connection, etc.).
func NewAutoFinder(config *AutoFinderConfig, db *sqlx.DB, githubClient *github.Client, antiSpam *NotificationSpamManager) (*AutoFinder, error) {
	// Validate inputs
	if githubClient == nil {
		return nil, fmt.Errorf("githubClient is required and cannot be nil")
	}

	if config == nil {
		config = DefaultAutoFinderConfig()
	}

	useDB := db != nil
	var fileStorage *FileStorage
	if !useDB {
		var err error
		fileStorage, err = NewFileStorage("")
		if err != nil {
			return nil, fmt.Errorf("failed to initialize file storage: %w", err)
		}
	}

	smartLimitsConfig := DefaultSmartLimitsConfig()
	smartLimiter := NewSmartLimiter(smartLimitsConfig, fileStorage, db, useDB)
	strategy := NewCommentStrategy(smartLimiter)

	af := &AutoFinder{
		config:       config,
		db:           db,
		githubClient: githubClient,
		antiSpam:     antiSpam,
		repoManager:  NewRepoManager(),
		scorer:       NewEnhancedScorer(),
		commentQueue: make([]CommentRequest, 0),
		fileStorage:  fileStorage,
		useDB:        useDB,
		smartLimiter: smartLimiter,
		strategy:     strategy,
	}

	if useDB {
		if err := af.initSchema(); err != nil {
			return nil, fmt.Errorf("failed to initialize auto finder schema: %w", err)
		}
	}

	return af, nil
}

// initSchema creates the necessary database tables and indexes for tracking comments,
// daily limits, and found issues. Called during initialization if using database backend.
func (af *AutoFinder) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS comment_history (
		id SERIAL PRIMARY KEY,
		repo VARCHAR(255) NOT NULL,
		issue_number INT NOT NULL,
		issue_url VARCHAR(500),
		commented_at TIMESTAMP DEFAULT NOW(),
		comment_text TEXT,
		score FLOAT,
		status VARCHAR(50) DEFAULT 'posted',
		UNIQUE(repo, issue_number)
	);

	CREATE TABLE IF NOT EXISTS daily_limits (
		id SERIAL PRIMARY KEY,
		date DATE NOT NULL UNIQUE,
		comments_count INT DEFAULT 0,
		last_comment_at TIMESTAMP,
		repos_commented TEXT[]
	);

	CREATE TABLE IF NOT EXISTS found_issues (
		id SERIAL PRIMARY KEY,
		repo VARCHAR(255) NOT NULL,
		issue_number INT NOT NULL,
		title TEXT,
		score FLOAT,
		found_at TIMESTAMP DEFAULT NOW(),
		status VARCHAR(50) DEFAULT 'found',
		UNIQUE(repo, issue_number)
	);

	CREATE TABLE IF NOT EXISTS finder_config (
		key VARCHAR(100) PRIMARY KEY,
		value TEXT,
		updated_at TIMESTAMP DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_comment_history_repo ON comment_history(repo);
	CREATE INDEX IF NOT EXISTS idx_comment_history_commented_at ON comment_history(commented_at);
	CREATE INDEX IF NOT EXISTS idx_found_issues_score ON found_issues(score DESC);
	CREATE INDEX IF NOT EXISTS idx_found_issues_status ON found_issues(status);
	`

	_, err := af.db.Exec(schema)
	return err
}

// Run executes the main auto-finder workflow: search for issues, score them,
// filter valid ones, optionally post comments, and send notifications.
// Returns an error if any critical operation fails (search, scoring, etc.).
func (af *AutoFinder) Run(ctx context.Context) error {
	if !af.config.Enabled {
		log.Println("[AutoFinder] Auto-finder is disabled")
		return nil
	}

	if !af.canCommentToday() {
		log.Println("[AutoFinder] Daily comment limit reached")
	}

	issues, err := af.searchIssues(ctx)
	if err != nil {
		return fmt.Errorf("failed to search issues: %w", err)
	}

	scored := af.scoreIssues(issues)
	valid := af.filterValidIssues(scored)

	sort.Slice(valid, func(i, j int) bool {
		return valid[i].Score.Total > valid[j].Score.Total
	})

	if af.config.AutoComment && len(valid) > 0 && af.canCommentToday() {
		for _, issue := range valid {
			if issue.Score.Total >= af.config.MinScoreToComment {
				if err := af.commentOnBestIssue(ctx, issue); err != nil {
					log.Printf("[AutoFinder] Failed to comment on issue: %v", err)
				} else {
					break
				}
			}
		}
	}

	af.sendNotifications(valid)

	log.Printf("[AutoFinder] Run complete. Found %d issues, %d valid", len(issues), len(valid))
	return nil
}

// Search finds and scores issues matching the criteria without posting comments.
// Returns a sorted list of qualified issues by score (highest first).
// Useful for previewing results before committing comments.
func (af *AutoFinder) Search(ctx context.Context) ([]ScoredIssue, error) {
	issues, err := af.searchIssues(ctx)
	if err != nil {
		return nil, err
	}

	scored := af.scoreIssues(issues)
	valid := af.filterValidIssues(scored)

	sort.Slice(valid, func(i, j int) bool {
		return valid[i].Score.Total > valid[j].Score.Total
	})

	return valid, nil
}

// searchIssues queries all enabled repositories for open, unassigned issues.
// Returns the list of issues found. Skips pull requests and closed issues.
// Logs errors per-repo but continues searching other repos.
// Filters out issues with linked PRs or PR mentions in the body.
func (af *AutoFinder) searchIssues(ctx context.Context) ([]*github.Issue, error) {
	var allIssues []*github.Issue
	repos := af.repoManager.GetEnabledRepos()

	for _, repo := range repos {
		opts := &github.IssueListByRepoOptions{
			State:     "open",
			Sort:      "created",
			Direction: "desc",
			ListOptions: github.ListOptions{
				PerPage: 30,
			},
		}

		issues, _, err := af.githubClient.Issues.ListByRepo(ctx, repo.Owner, repo.Name, opts)
		if err != nil {
			log.Printf("[AutoFinder] Error fetching issues for %s/%s: %v", repo.Owner, repo.Name, err)
			continue
		}

		for _, issue := range issues {
			if issue.IsPullRequest() {
				continue
			}
			if issue.GetState() != "open" {
				continue
			}
			if len(issue.Assignees) > 0 {
				continue
			}
			// Skip issues with linked PRs
			if issue.PullRequestLinks != nil {
				log.Printf("[AutoFinder] Skipping %s/%s#%d - has linked PR", repo.Owner, repo.Name, issue.GetNumber())
				continue
			}
			// Skip issues mentioning PRs in the body
			if issue.Body != nil && strings.Contains(*issue.Body, "#") {
				// Check if it looks like a PR reference pattern
				issueBody := *issue.Body
				if strings.Contains(issueBody, "PR ") || strings.Contains(issueBody, "pull request") ||
					strings.Contains(issueBody, "#[0-9]") {
					log.Printf("[AutoFinder] Skipping %s/%s#%d - mentions PR in body", repo.Owner, repo.Name, issue.GetNumber())
					continue
				}
			}
			allIssues = append(allIssues, issue)
		}
	}

	return allIssues, nil
}

// CheckIssueForActivePR verifies if an issue has any active PR work.
// Returns true if issue should be skipped (has linked PR or is mentioned in discussions).
func (af *AutoFinder) CheckIssueForActivePR(issue *github.Issue) bool {
	if issue.PullRequestLinks != nil {
		return true
	}
	// Check if any comments mention PR numbers
	if issue.Body != nil && strings.Contains(*issue.Body, "#") {
		return true
	}
	return false
}

// scoreIssues calculates quality scores for each issue based on issue details,
// project configuration, and scoring algorithms. Returns list of scored issues
// with breakdowns and metadata extracted from GitHub data.
func (af *AutoFinder) scoreIssues(issues []*github.Issue) []ScoredIssue {
	var scored []ScoredIssue

	for _, issue := range issues {
		repoName := ""
		owner := ""
		if parts := strings.Split(issue.GetRepositoryURL(), "/"); len(parts) >= 5 {
			owner = parts[len(parts)-2]
			repoName = parts[len(parts)-1]
		}

		project := Project{
			Org:  owner,
			Name: repoName,
		}

		repoConfig := af.repoManager.GetRepo(owner, repoName)
		if repoConfig != nil {
			project.Category = repoConfig.Category
			project.Stars = repoConfig.MinStars
		}

		issueScore := CalculateScore(issue, &project, nil)

		labels := make([]string, 0, len(issue.Labels))
		for _, label := range issue.Labels {
			labels = append(labels, label.GetName())
		}

		scoredIssue := ScoredIssue{
			Issue:   issue,
			Project: project,
			Score:   issueScore,
			IssueData: Issue{
				Project:     project,
				Title:       issue.GetTitle(),
				URL:         issue.GetHTMLURL(),
				Number:      issue.GetNumber(),
				Score:       issueScore.Total,
				CreatedAt:   issue.GetCreatedAt().Time,
				Comments:    issue.GetComments(),
				Labels:      labels,
				IsGoodFirst: hasGoodFirstIssueLabel(issue.Labels),
			},
		}

		scored = append(scored, scoredIssue)
	}

	return scored
}

// filterValidIssues filters scored issues based on quality thresholds and spam detection.
// Excludes issues below minimum score, flagged by anti-spam system, or having active PRs.
// Returns only issues that pass all validation checks.
func (af *AutoFinder) filterValidIssues(issues []ScoredIssue) []ScoredIssue {
	var valid []ScoredIssue

	for _, issue := range issues {
		if issue.Score.Total < af.config.MinScoreToComment {
			continue
		}

		if af.antiSpam != nil {
			canNotify, _ := af.antiSpam.CanNotify(issue.Project.Name, issue.IssueData.URL)
			if !canNotify {
				continue
			}
		}

		// Skip issues that have active PRs
		if af.CheckIssueForActivePR(issue.Issue) {
			log.Printf("[AutoFinder] Skipping %s/%s#%d - has active PR", issue.Project.Org, issue.Project.Name, issue.Issue.GetNumber())
			continue
		}

		valid = append(valid, issue)
	}

	return valid
}

// canCommentToday checks if the daily comment limit has been reached.
// Uses database if available, otherwise falls back to file storage.
// Returns true if more comments can be posted today.
func (af *AutoFinder) canCommentToday() bool {
	if af.useDB {
		today := time.Now().Format("2006-01-02")

		var count int
		err := af.db.Get(&count, "SELECT comments_count FROM daily_limits WHERE date = $1", today)
		if err == sql.ErrNoRows {
			return true
		}
		if err != nil {
			log.Printf("[AutoFinder] Error checking daily limits: %v", err)
			return true
		}

		if count >= af.config.MaxCommentsPerDay {
			return false
		}

		return true
	}

	if af.fileStorage != nil {
		return af.fileStorage.CanCommentToday(af.config.MaxCommentsPerDay)
	}
	return true
}

// canCommentOnRepo checks if a comment has already been posted to a repo today.
// Enforces MaxCommentsPerRepo limit per day.
// Returns true if another comment can be posted to this repo today.
func (af *AutoFinder) canCommentOnRepo(repo string) bool {
	if af.useDB {
		today := time.Now().Format("2006-01-02")

		var repos []string
		err := af.db.Select(&repos, "SELECT repos_commented FROM daily_limits WHERE date = $1", today)
		if err != nil && err != sql.ErrNoRows {
			log.Printf("[AutoFinder] Error checking repos commented: %v", err)
			return true
		}

		for _, r := range repos {
			if r == repo {
				return false
			}
		}

		return true
	}

	if af.fileStorage != nil {
		return af.fileStorage.CanCommentOnRepo(repo)
	}
	return true
}

// commentOnBestIssue generates and posts a comment on the specified issue.
// Uses SmartCommentGenerator to validate issue state and prevent posting on
// issues with existing solutions. Checks repo comment limits, generates comment text,
// posts to GitHub, and records the comment in history. Returns an error if posting fails.
func (af *AutoFinder) commentOnBestIssue(ctx context.Context, issue ScoredIssue) error {
	if !af.canCommentOnRepo(issue.Project.Name) {
		return fmt.Errorf("already commented on repo %s today", issue.Project.Name)
	}

	// Generate comment using smart generator to validate issue state
	scg := NewSmartCommentGenerator()
	issueDetails := IssueDetails{
		Title:        issue.Issue.GetTitle(),
		Body:         issue.Issue.GetBody(),
		Number:       issue.Issue.GetNumber(),
		URL:          issue.Issue.GetHTMLURL(),
		ProjectOwner: issue.Project.Org,
		ProjectName:  issue.Project.Name,
		HasLinkedPR:  issue.Issue.PullRequestLinks != nil,
	}

	smartComment, err := scg.GenerateSmartComment(issueDetails)
	if err != nil {
		if strings.Contains(err.Error(), "solution") || strings.Contains(err.Error(), "already has") {
			log.Printf("[AutoFinder] Skipping %s/%s#%d - %v", issue.Project.Org, issue.Project.Name, issue.Issue.GetNumber(), err)
			return nil
		}
		return err
	}

	// Use smartComment.Body instead of af.generateComment()
	comment := smartComment.Body

	_, _, err = af.githubClient.Issues.CreateComment(ctx, issue.Project.Org, issue.Project.Name, issue.Issue.GetNumber(), &github.IssueComment{
		Body: github.String(comment),
	})
	if err != nil {
		return fmt.Errorf("failed to post comment: %w", err)
	}

	if err := af.recordComment(issue, comment); err != nil {
		log.Printf("[AutoFinder] Failed to record comment: %v", err)
	}

	log.Printf("[AutoFinder] Successfully commented on %s/%s#%d", issue.Project.Org, issue.Project.Name, issue.Issue.GetNumber())
	return nil
}

// generateComment creates a generic comment for posting on an issue.
// References issue context like reproduction steps and maintainer confirmation.
// Note: This is a fallback generic generator; smart comments use SmartCommentGenerator.
func (af *AutoFinder) generateComment(issue ScoredIssue) string {
	var sb strings.Builder

	sb.WriteString("Hi! I noticed this issue and would like to help contribute.\n\n")

	if issue.Score.Breakdown.HasReproSteps > 0.5 {
		sb.WriteString("The reproduction steps are clear. ")
	}

	if issue.Score.Breakdown.MaintainerConfirmed > 0.5 {
		sb.WriteString("I see this has been confirmed by maintainers. ")
	}

	sb.WriteString("\nI'll start working on this and provide an update soon.\n\n")
	sb.WriteString("Please let me know if anyone is already working on this or if there are any specific guidelines I should follow.")

	return sb.String()
}

// recordComment stores a posted comment in database or file storage for history tracking.
// Records the comment in comment_history, updates daily_limits, and stores in found_issues.
// Returns an error if recording fails (but does not affect already-posted comment).
func (af *AutoFinder) recordComment(issue ScoredIssue, comment string) error {
	if af.useDB {
		today := time.Now().Format("2006-01-02")
		now := time.Now()

		tx, err := af.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		_, err = tx.Exec(`
			INSERT INTO comment_history (repo, issue_number, issue_url, comment_text, score, commented_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (repo, issue_number) DO UPDATE SET commented_at = $6, comment_text = $4
		`, issue.Project.Name, issue.Issue.GetNumber(), issue.IssueData.URL, comment, issue.Score.Total, now)
		if err != nil {
			return err
		}

		_, err = tx.Exec(`
			INSERT INTO daily_limits (date, comments_count, last_comment_at, repos_commented)
			VALUES ($1, 1, $2, ARRAY[$3])
			ON CONFLICT (date) DO UPDATE SET
				comments_count = daily_limits.comments_count + 1,
				last_comment_at = $2,
				repos_commented = array_append(daily_limits.repos_commented, $3)
		`, today, now, issue.Project.Name)
		if err != nil {
			return err
		}

		_, err = tx.Exec(`
			INSERT INTO found_issues (repo, issue_number, title, score, status)
			VALUES ($1, $2, $3, $4, 'commented')
			ON CONFLICT (repo, issue_number) DO UPDATE SET status = 'commented', score = $4
		`, issue.Project.Name, issue.Issue.GetNumber(), issue.Issue.GetTitle(), issue.Score.Total)

		return tx.Commit()
	}

	if af.fileStorage != nil {
		record := FileCommentRecord{
			Repo:        issue.Project.Name,
			IssueNumber: issue.Issue.GetNumber(),
			IssueURL:    issue.IssueData.URL,
			CommentedAt: time.Now(),
			CommentText: comment,
			Score:       issue.Score.Total,
		}
		if err := af.fileStorage.SaveComment(record); err != nil {
			return err
		}
		if err := af.fileStorage.RecordComment(issue.Project.Name); err != nil {
			return err
		}
		foundIssue := FileFoundIssue{
			Repo:        issue.Project.Name,
			IssueNumber: issue.Issue.GetNumber(),
			Title:       issue.Issue.GetTitle(),
			Score:       issue.Score.Total,
			FoundAt:     time.Now(),
			Status:      "commented",
		}
		return af.fileStorage.SaveFoundIssue(foundIssue)
	}

	return nil
}

// recordFoundIssue logs an issue as found but not yet commented on.
// Used to track discovered issues without posting comments.
func (af *AutoFinder) recordFoundIssue(issue ScoredIssue) error {
	_, err := af.db.Exec(`
		INSERT INTO found_issues (repo, issue_number, title, score, status)
		VALUES ($1, $2, $3, $4, 'found')
		ON CONFLICT (repo, issue_number) DO UPDATE SET score = $4
	`, issue.Project.Name, issue.Issue.GetNumber(), issue.Issue.GetTitle(), issue.Score.Total)
	return err
}

// sendNotifications logs found issues and sends notifications if configured.
// Logs the top qualifying issues with scores and URLs for user review.
func (af *AutoFinder) sendNotifications(issues []ScoredIssue) {
	if len(issues) == 0 {
		return
	}

	if af.config.NotifyOnFind {
		log.Printf("[AutoFinder] Found %d qualifying issues", len(issues))
		for i, issue := range issues {
			if i >= 5 {
				break
			}
			log.Printf("[AutoFinder]   - %s (%.2f) - %s", issue.IssueData.Title, issue.Score.Total, issue.IssueData.URL)
		}
	}
}

// GetStatus returns the current status of the auto-finder including enabled state,
// comment count for today, and number of issues found.
// Returns nil and an error if status retrieval fails.
func (af *AutoFinder) GetStatus() (*AutoFinderStatus, error) {
	status := &AutoFinderStatus{
		Enabled:     af.config.Enabled,
		AutoComment: af.config.AutoComment,
	}

	if af.useDB {
		today := time.Now().Format("2006-01-02")

		err := af.db.Get(&status.CommentsToday, "SELECT COALESCE(comments_count, 0) FROM daily_limits WHERE date = $1", today)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}

		err = af.db.Get(&status.FoundIssuesCount, "SELECT COUNT(*) FROM found_issues WHERE found_at >= CURRENT_DATE")
		if err != nil {
			status.FoundIssuesCount = 0
		}
	} else if af.fileStorage != nil {
		count, err := af.fileStorage.GetTodayCommentsCount()
		if err != nil {
			status.CommentsToday = 0
		} else {
			status.CommentsToday = count
		}

		pending, err := af.fileStorage.GetPendingIssuesCount(af.config.MinScoreToComment)
		if err != nil {
			status.FoundIssuesCount = 0
		} else {
			status.FoundIssuesCount = pending
		}
	}

	status.MaxCommentsPerDay = af.config.MaxCommentsPerDay
	status.MinScoreToComment = af.config.MinScoreToComment

	return status, nil
}

// GetHistory retrieves the history of posted comments up to the specified limit.
// Uses database if available, otherwise file storage.
// Returns the comment records sorted by most recent first.
func (af *AutoFinder) GetHistory(limit int) ([]FileCommentRecord, error) {
	if af.useDB {
		var records []FileCommentRecord
		err := af.db.Select(&records, `
			SELECT id, repo, issue_number, commented_at, comment_text, score
			FROM comment_history
			ORDER BY commented_at DESC
			LIMIT $1
		`, limit)
		return records, err
	}

	if af.fileStorage != nil {
		history, err := af.fileStorage.LoadHistory()
		if err != nil {
			return nil, err
		}
		if len(history) > limit {
			history = history[:limit]
		}
		return history, nil
	}

	return []FileCommentRecord{}, nil
}

// Enable activates the auto-finder system by updating configuration in database.
// Persists the enabled state so it survives restart.
func (af *AutoFinder) Enable() error {
	af.config.Enabled = true
	_, err := af.db.Exec(`
		INSERT INTO finder_config (key, value, updated_at)
		VALUES ('enabled', 'true', NOW())
		ON CONFLICT (key) DO UPDATE SET value = 'true', updated_at = NOW()
	`)
	return err
}

// Disable deactivates the auto-finder system by updating configuration in database.
// Persists the disabled state so it survives restart.
func (af *AutoFinder) Disable() error {
	af.config.Enabled = false
	_, err := af.db.Exec(`
		INSERT INTO finder_config (key, value, updated_at)
		VALUES ('enabled', 'false', NOW())
		ON CONFLICT (key) DO UPDATE SET value = 'false', updated_at = NOW()
	`)
	return err
}

// SetAutoComment enables or disables automatic comment posting in the database configuration.
func (af *AutoFinder) SetAutoComment(enabled bool) error {
	af.config.AutoComment = enabled
	val := "false"
	if enabled {
		val = "true"
	}
	_, err := af.db.Exec(`
		INSERT INTO finder_config (key, value, updated_at)
		VALUES ('auto_comment', $1, NOW())
		ON CONFLICT (key) DO UPDATE SET value = $1, updated_at = NOW()
	`, val)
	return err
}

// AutoFinderStatus contains current operational metrics of the auto-finder.
type AutoFinderStatus struct {
	Enabled           bool    // Whether auto-finder is enabled
	AutoComment       bool    // Whether auto-comment is enabled
	CommentsToday     int     // Number of comments posted today
	MaxCommentsPerDay int     // Maximum allowed comments per day
	FoundIssuesCount  int     // Number of qualifying issues found
	MinScoreToComment float64 // Current minimum score threshold
}

// LoadAutoFinderConfigFromEnv loads auto-finder configuration from environment variables.
// Allows runtime configuration without code changes. Falls back to defaults for unset vars.
func LoadAutoFinderConfigFromEnv() *AutoFinderConfig {
	config := DefaultAutoFinderConfig()

	if enabled := getEnvBool("AUTO_FINDER_ENABLED", false); enabled {
		config.Enabled = true
	}

	if autoComment := getEnvBool("AUTO_COMMENT", false); autoComment {
		config.AutoComment = true
	}

	if maxComments := getEnvInt("MAX_COMMENTS_PER_DAY", 3); maxComments > 0 {
		config.MaxCommentsPerDay = maxComments
	}

	if maxPerRepo := getEnvInt("MAX_COMMENTS_PER_REPO", 1); maxPerRepo > 0 {
		config.MaxCommentsPerRepo = maxPerRepo
	}

	if minScore := getEnvFloat("MIN_SCORE_TO_COMMENT", 0.75); minScore > 0 {
		config.MinScoreToComment = minScore
	}

	if minHours := getEnvInt("MIN_HOURS_BETWEEN_COMMENTS", 2); minHours > 0 {
		config.MinHoursBetweenComments = minHours
	}

	config.NotifyOnComment = getEnvBool("NOTIFY_ON_COMMENT", true)
	config.NotifyOnFind = getEnvBool("NOTIFY_ON_FIND", true)
	config.EmailResults = getEnvBool("EMAIL_RESULTS", false)

	return config
}

// getEnvBool retrieves a boolean value from environment variable with default fallback.
func getEnvBool(key string, defaultVal bool) bool {
	val := strings.ToLower(strings.TrimSpace(getEnvStr(key, "")))
	if val == "true" || val == "1" || val == "yes" {
		return true
	}
	if val == "false" || val == "0" || val == "no" {
		return false
	}
	return defaultVal
}

// getEnvInt retrieves an integer value from environment variable with default fallback.
func getEnvInt(key string, defaultVal int) int {
	val := getEnvStr(key, "")
	if val == "" {
		return defaultVal
	}
	var result int
	fmt.Sscanf(val, "%d", &result)
	if result == 0 {
		return defaultVal
	}
	return result
}

// getEnvFloat retrieves a float value from environment variable with default fallback.
func getEnvFloat(key string, defaultVal float64) float64 {
	val := getEnvStr(key, "")
	if val == "" {
		return defaultVal
	}
	var result float64
	fmt.Sscanf(val, "%f", &result)
	if result == 0 {
		return defaultVal
	}
	return result
}

// getEnvStr retrieves a string value from environment variable with default fallback.
func getEnvStr(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// AutoCommentResult represents the outcome of attempting to post a comment on an issue.
type AutoCommentResult struct {
	Repo        string // Repository name
	IssueNumber int    // Issue number
	Success     bool   // Whether comment was successfully posted
	Error       string // Error message if posting failed
}

// Preview generates a preview of comments that would be posted without actually posting them.
// Useful for testing and reviewing comments before committing. Returns preview objects with
// comment text, score, and reason for posting decision.
func (af *AutoFinder) Preview(ctx context.Context) ([]CommentPreview, error) {
	issues, err := af.Search(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to search issues: %w", err)
	}

	selected := af.strategy.SelectIssuesToComment(issues)

	var previews []CommentPreview
	for _, issue := range selected {
		comment := af.generateComment(issue)
		canComment, reason := af.smartLimiter.CanComment(issue.Project.Name, issue.Score.Total)

		preview := CommentPreview{
			Repo:        issue.Project.Name,
			IssueNumber: issue.IssueData.Number,
			Title:       issue.IssueData.Title,
			Score:       issue.Score.Total,
			Comment:     comment,
			URL:         issue.IssueData.URL,
			Reason:      reason,
		}

		if !canComment {
			preview.Reason = reason
		}

		previews = append(previews, preview)
	}

	return previews, nil
}

// CommitComments posts all previewed comments to GitHub after final validation.
// Checks smart limiter constraints, respects rate limits, and records each comment.
// Returns results for each issue including success status and error messages.
func (af *AutoFinder) CommitComments(ctx context.Context) ([]AutoCommentResult, error) {
	previews, err := af.Preview(ctx)
	if err != nil {
		return nil, err
	}

	var results []AutoCommentResult

	for _, preview := range previews {
		result := AutoCommentResult{
			Repo:        preview.Repo,
			IssueNumber: preview.IssueNumber,
			Success:     false,
		}

		canComment, reason := af.smartLimiter.CanComment(preview.Repo, preview.Score)
		if !canComment {
			result.Error = reason
			results = append(results, result)
			continue
		}

		org := preview.Repo
		if strings.Contains(preview.URL, "github.com/") {
			parts := strings.Split(preview.URL, "/")
			if len(parts) >= 5 {
				org = parts[3]
			}
		}

		_, _, err := af.githubClient.Issues.CreateComment(ctx, org, preview.Repo, preview.IssueNumber, &github.IssueComment{
			Body: github.String(preview.Comment),
		})

		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}

		if err := af.smartLimiter.RecordComment(preview.Repo); err != nil {
			log.Printf("[AutoFinder] Failed to record comment: %v", err)
		}

		result.Success = true
		results = append(results, result)

		log.Printf("[AutoFinder] Successfully commented on %s/%s#%d", org, preview.Repo, preview.IssueNumber)
	}

	return results, nil
}

// GetSmartLimiterStatus returns the current status of the smart limiter component.
// Shows remaining comments today and other rate limit information.
func (af *AutoFinder) GetSmartLimiterStatus() *SmartLimiterStatus {
	if af.smartLimiter == nil {
		return nil
	}
	return af.smartLimiter.GetStatus()
}
