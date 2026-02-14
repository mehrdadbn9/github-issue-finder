package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/go-github/v58/github"
	"github.com/jmoiron/sqlx"
)

type CommentManager struct {
	client *github.Client
	db     *sqlx.DB
	mu     sync.RWMutex

	rateLimits map[string]time.Time
	config     *AntiSpamConfig
}

type AntiSpamConfig struct {
	MaxCommentsPerHour    int           `json:"max_comments_per_hour"`
	MinIntervalBetween    time.Duration `json:"min_interval_between"`
	MaxCommentsPerProject int           `json:"max_comments_per_project"`
	CooldownPeriod        time.Duration `json:"cooldown_period"`
	DailyLimit            int           `json:"daily_limit"`
}

type CommentRecord struct {
	ID           int       `db:"id"`
	IssueURL     string    `db:"issue_url"`
	ProjectName  string    `db:"project_name"`
	CommentedAt  time.Time `db:"commented_at"`
	CommentBody  string    `db:"comment_body"`
	Success      bool      `db:"success"`
	ErrorMessage string    `db:"error_message"`
}

func NewCommentManager(client *github.Client, db *sqlx.DB, config *AntiSpamConfig) *CommentManager {
	if config == nil {
		config = &AntiSpamConfig{
			MaxCommentsPerHour:    3,
			MinIntervalBetween:    10 * time.Minute,
			MaxCommentsPerProject: 1,
			CooldownPeriod:        48 * time.Hour,
			DailyLimit:            10,
		}
	}

	cm := &CommentManager{
		client:     client,
		db:         db,
		rateLimits: make(map[string]time.Time),
		config:     config,
	}

	cm.initTables()

	return cm
}

func (cm *CommentManager) initTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS comment_history (
		id SERIAL PRIMARY KEY,
		issue_url TEXT NOT NULL,
		project_name TEXT NOT NULL,
		commented_at TIMESTAMP NOT NULL DEFAULT NOW(),
		comment_body TEXT,
		success BOOLEAN DEFAULT true,
		error_message TEXT,
		github_user TEXT,
		CONSTRAINT unique_issue_comment UNIQUE (issue_url)
	);

	CREATE INDEX IF NOT EXISTS idx_comment_history_project ON comment_history(project_name);
	CREATE INDEX IF NOT EXISTS idx_comment_history_time ON comment_history(commented_at);
	`
	_, err := cm.db.Exec(schema)
	return err
}

func (cm *CommentManager) CanComment(ctx context.Context, issueURL, projectName string) (bool, string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if lastComment, exists := cm.rateLimits["global"]; exists {
		if time.Since(lastComment) < cm.config.MinIntervalBetween {
			waitTime := cm.config.MinIntervalBetween - time.Since(lastComment)
			return false, fmt.Sprintf("Rate limit: wait %v before next comment", waitTime.Round(time.Second))
		}
	}

	var count int
	err := cm.db.Get(&count, "SELECT COUNT(*) FROM comment_history WHERE commented_at > NOW() - INTERVAL '1 hour'")
	if err == nil && count >= cm.config.MaxCommentsPerHour {
		return false, fmt.Sprintf("Hourly limit reached (%d/%d comments)", count, cm.config.MaxCommentsPerHour)
	}

	err = cm.db.Get(&count, "SELECT COUNT(*) FROM comment_history WHERE commented_at > NOW() - INTERVAL '24 hours'")
	if err == nil && count >= cm.config.DailyLimit {
		return false, fmt.Sprintf("Daily limit reached (%d/%d comments)", count, cm.config.DailyLimit)
	}

	err = cm.db.Get(&count, "SELECT COUNT(*) FROM comment_history WHERE project_name = $1 AND commented_at > NOW() - INTERVAL '24 hours'", projectName)
	if err == nil && count >= cm.config.MaxCommentsPerProject {
		return false, fmt.Sprintf("Project limit for %s reached (%d/%d)", projectName, count, cm.config.MaxCommentsPerProject)
	}

	var alreadyCommented bool
	err = cm.db.Get(&alreadyCommented, "SELECT EXISTS(SELECT 1 FROM comment_history WHERE issue_url = $1)", issueURL)
	if err == nil && alreadyCommented {
		return false, "Already commented on this issue"
	}

	var commentedAt *time.Time
	err = cm.db.Get(&commentedAt, "SELECT commented_at FROM comment_history WHERE issue_url = $1", issueURL)
	if err == nil && commentedAt != nil {
		if time.Since(*commentedAt) < cm.config.CooldownPeriod {
			return false, fmt.Sprintf("Cooldown period active, wait %v", cm.config.CooldownPeriod-time.Since(*commentedAt))
		}
	}

	return true, ""
}

func (cm *CommentManager) PostComment(ctx context.Context, owner, repo string, issueNumber int, body string) error {
	issueURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, issueNumber)

	canComment, reason := cm.CanComment(ctx, issueURL, repo)
	if !canComment {
		return fmt.Errorf("cannot comment: %s", reason)
	}

	comment, _, err := cm.client.Issues.CreateComment(ctx, owner, repo, issueNumber, &github.IssueComment{
		Body: github.String(body),
	})
	if err != nil {
		cm.recordComment(issueURL, repo, body, false, err.Error())
		return fmt.Errorf("failed to post comment: %w", err)
	}

	cm.recordComment(issueURL, repo, body, true, "")

	cm.mu.Lock()
	cm.rateLimits["global"] = time.Now()
	cm.mu.Unlock()

	log.Printf("Posted comment on %s/%s#%d (ID: %d)", owner, repo, issueNumber, comment.GetID())

	return nil
}

func (cm *CommentManager) recordComment(issueURL, projectName, body string, success bool, errMsg string) {
	record := CommentRecord{
		IssueURL:     issueURL,
		ProjectName:  projectName,
		CommentedAt:  time.Now(),
		CommentBody:  body,
		Success:      success,
		ErrorMessage: errMsg,
	}

	_, err := cm.db.NamedExec(`
		INSERT INTO comment_history (issue_url, project_name, commented_at, comment_body, success, error_message)
		VALUES (:issue_url, :project_name, :commented_at, :comment_body, :success, :error_message)
		ON CONFLICT (issue_url) DO UPDATE SET 
			commented_at = EXCLUDED.commented_at,
			comment_body = EXCLUDED.comment_body,
			success = EXCLUDED.success,
			error_message = EXCLUDED.error_message
	`, record)

	if err != nil {
		log.Printf("Failed to record comment: %v", err)
	}
}

func (cm *CommentManager) GetCommentHistory(limit int) ([]CommentRecord, error) {
	var records []CommentRecord
	err := cm.db.Select(&records, "SELECT * FROM comment_history ORDER BY commented_at DESC LIMIT $1", limit)
	return records, err
}

func (cm *CommentManager) GetStats() (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	var totalComments int
	err := cm.db.Get(&totalComments, "SELECT COUNT(*) FROM comment_history WHERE success = true")
	if err == nil {
		stats["total_successful_comments"] = totalComments
	}

	var failedComments int
	err = cm.db.Get(&failedComments, "SELECT COUNT(*) FROM comment_history WHERE success = false")
	if err == nil {
		stats["total_failed_comments"] = failedComments
	}

	var commentsLastHour int
	err = cm.db.Get(&commentsLastHour, "SELECT COUNT(*) FROM comment_history WHERE commented_at > NOW() - INTERVAL '1 hour'")
	if err == nil {
		stats["comments_last_hour"] = commentsLastHour
	}

	var commentsToday int
	err = cm.db.Get(&commentsToday, "SELECT COUNT(*) FROM comment_history WHERE commented_at > NOW() - INTERVAL '24 hours'")
	if err == nil {
		stats["comments_today"] = commentsToday
	}

	stats["hourly_limit"] = cm.config.MaxCommentsPerHour
	stats["daily_limit"] = cm.config.DailyLimit
	stats["remaining_hourly"] = cm.config.MaxCommentsPerHour - commentsLastHour
	stats["remaining_daily"] = cm.config.DailyLimit - commentsToday

	return stats, nil
}

func (cm *CommentManager) IsAlreadyCommented(issueURL string) bool {
	var exists bool
	err := cm.db.Get(&exists, "SELECT EXISTS(SELECT 1 FROM comment_history WHERE issue_url = $1 AND success = true)", issueURL)
	return err == nil && exists
}

func (cm *CommentManager) GetNextAvailableTime() time.Time {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if lastComment, exists := cm.rateLimits["global"]; exists {
		return lastComment.Add(cm.config.MinIntervalBetween)
	}
	return time.Now()
}
