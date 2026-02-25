package main

import (
	"database/sql"
	"fmt"
	"time"
)

type WorkStatus string

const (
	StatusNew             WorkStatus = "new"
	StatusNotified        WorkStatus = "notified"
	StatusAskedAssignment WorkStatus = "asked_assignment"
	StatusAssigned        WorkStatus = "assigned"
	StatusInProgress      WorkStatus = "in_progress"
	StatusPRSubmitted     WorkStatus = "pr_submitted"
	StatusCompleted       WorkStatus = "completed"
	StatusAbandoned       WorkStatus = "abandoned"
	StatusInterested      WorkStatus = "interested"
)

type TrackedIssue struct {
	ID                int64
	IssueURL          string
	IssueTitle        string
	ProjectOrg        string
	ProjectName       string
	IssueNumber       int
	Status            WorkStatus
	Notes             string
	Score             float64
	Labels            string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	StartedAt         *time.Time
	CompletedAt       *time.Time
	NotifiedAt        *time.Time
	AssignmentAskedAt *time.Time
	HasGoodFirst      bool
	HasConfirmed      bool
	HasAssignee       bool
	HasPR             bool
}

type IssueTracker struct {
	db *sql.DB
}

func NewIssueTracker(db *sql.DB) (*IssueTracker, error) {
	tracker := &IssueTracker{db: db}
	if err := tracker.initDB(); err != nil {
		return nil, err
	}
	return tracker, nil
}

func (t *IssueTracker) initDB() error {
	schema := `
	CREATE TABLE IF NOT EXISTS tracked_issues (
		id SERIAL PRIMARY KEY,
		issue_url TEXT UNIQUE NOT NULL,
		issue_title TEXT NOT NULL,
		project_org TEXT NOT NULL,
		project_name TEXT NOT NULL,
		issue_number INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT 'new',
		notes TEXT,
		score FLOAT,
		labels TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		started_at TIMESTAMP,
		completed_at TIMESTAMP,
		notified_at TIMESTAMP,
		assignment_asked_at TIMESTAMP,
		has_good_first BOOLEAN DEFAULT FALSE,
		has_confirmed BOOLEAN DEFAULT FALSE,
		has_assignee BOOLEAN DEFAULT FALSE,
		has_pr BOOLEAN DEFAULT FALSE
	);

	CREATE INDEX IF NOT EXISTS idx_tracked_issues_status ON tracked_issues(status);
	CREATE INDEX IF NOT EXISTS idx_tracked_issues_url ON tracked_issues(issue_url);
	CREATE INDEX IF NOT EXISTS idx_tracked_issues_notified ON tracked_issues(notified_at);
	`

	_, err := t.db.Exec(schema)
	return err
}

func (t *IssueTracker) AddIssue(issue *TrackedIssue) error {
	query := `
	INSERT INTO tracked_issues (issue_url, issue_title, project_org, project_name, issue_number, status, notes, score, labels, has_good_first, has_confirmed, has_assignee, has_pr)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	ON CONFLICT (issue_url) DO UPDATE SET
		issue_title = EXCLUDED.issue_title,
		status = EXCLUDED.status,
		notes = EXCLUDED.notes,
		score = EXCLUDED.score,
		labels = EXCLUDED.labels,
		has_good_first = EXCLUDED.has_good_first,
		has_confirmed = EXCLUDED.has_confirmed,
		has_assignee = EXCLUDED.has_assignee,
		has_pr = EXCLUDED.has_pr,
		updated_at = CURRENT_TIMESTAMP
	`

	_, err := t.db.Exec(query,
		issue.IssueURL,
		issue.IssueTitle,
		issue.ProjectOrg,
		issue.ProjectName,
		issue.IssueNumber,
		issue.Status,
		issue.Notes,
		issue.Score,
		issue.Labels,
		issue.HasGoodFirst,
		issue.HasConfirmed,
		issue.HasAssignee,
		issue.HasPR,
	)
	return err
}

func (t *IssueTracker) UpdateStatus(issueURL string, status WorkStatus) error {
	var query string
	var args []interface{}

	now := time.Now()

	switch status {
	case StatusInProgress:
		query = `
		UPDATE tracked_issues 
		SET status = $1, started_at = $2, updated_at = $2 
		WHERE issue_url = $3`
		args = []interface{}{status, now, issueURL}
	case StatusCompleted:
		query = `
		UPDATE tracked_issues 
		SET status = $1, completed_at = $2, updated_at = $2 
		WHERE issue_url = $3`
		args = []interface{}{status, now, issueURL}
	case StatusAbandoned:
		query = `
		UPDATE tracked_issues 
		SET status = $1, updated_at = $2 
		WHERE issue_url = $3`
		args = []interface{}{status, now, issueURL}
	default:
		query = `
		UPDATE tracked_issues 
		SET status = $1, updated_at = $2 
		WHERE issue_url = $3`
		args = []interface{}{status, now, issueURL}
	}

	result, err := t.db.Exec(query, args...)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("issue not found: %s", issueURL)
	}

	return nil
}

func (t *IssueTracker) UpdateNotes(issueURL, notes string) error {
	query := `
	UPDATE tracked_issues 
	SET notes = $1, updated_at = $2 
	WHERE issue_url = $3`

	result, err := t.db.Exec(query, notes, time.Now(), issueURL)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("issue not found: %s", issueURL)
	}

	return nil
}

func (t *IssueTracker) GetIssue(issueURL string) (*TrackedIssue, error) {
	query := `
	SELECT id, issue_url, issue_title, project_org, project_name, issue_number, 
	       status, notes, score, labels, created_at, updated_at, started_at, completed_at
	FROM tracked_issues 
	WHERE issue_url = $1`

	issue := &TrackedIssue{}
	err := t.db.QueryRow(query, issueURL).Scan(
		&issue.ID,
		&issue.IssueURL,
		&issue.IssueTitle,
		&issue.ProjectOrg,
		&issue.ProjectName,
		&issue.IssueNumber,
		&issue.Status,
		&issue.Notes,
		&issue.Score,
		&issue.Labels,
		&issue.CreatedAt,
		&issue.UpdatedAt,
		&issue.StartedAt,
		&issue.CompletedAt,
	)
	if err != nil {
		return nil, err
	}

	return issue, nil
}

func (t *IssueTracker) GetByStatus(status WorkStatus) ([]TrackedIssue, error) {
	query := `
	SELECT id, issue_url, issue_title, project_org, project_name, issue_number, 
	       status, notes, score, labels, created_at, updated_at, started_at, completed_at
	FROM tracked_issues 
	WHERE status = $1
	ORDER BY updated_at DESC`

	rows, err := t.db.Query(query, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var issues []TrackedIssue
	for rows.Next() {
		var issue TrackedIssue
		err := rows.Scan(
			&issue.ID,
			&issue.IssueURL,
			&issue.IssueTitle,
			&issue.ProjectOrg,
			&issue.ProjectName,
			&issue.IssueNumber,
			&issue.Status,
			&issue.Notes,
			&issue.Score,
			&issue.Labels,
			&issue.CreatedAt,
			&issue.UpdatedAt,
			&issue.StartedAt,
			&issue.CompletedAt,
		)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}

	return issues, nil
}

func (t *IssueTracker) GetAll() ([]TrackedIssue, error) {
	query := `
	SELECT id, issue_url, issue_title, project_org, project_name, issue_number, 
	       status, notes, score, labels, created_at, updated_at, started_at, completed_at
	FROM tracked_issues 
	ORDER BY updated_at DESC`

	rows, err := t.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var issues []TrackedIssue
	for rows.Next() {
		var issue TrackedIssue
		err := rows.Scan(
			&issue.ID,
			&issue.IssueURL,
			&issue.IssueTitle,
			&issue.ProjectOrg,
			&issue.ProjectName,
			&issue.IssueNumber,
			&issue.Status,
			&issue.Notes,
			&issue.Score,
			&issue.Labels,
			&issue.CreatedAt,
			&issue.UpdatedAt,
			&issue.StartedAt,
			&issue.CompletedAt,
		)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}

	return issues, nil
}

func (t *IssueTracker) GetActiveCount() (int, error) {
	query := `
	SELECT COUNT(*) FROM tracked_issues 
	WHERE status IN ('new', 'notified', 'asked_assignment', 'assigned', 'in_progress')`

	var count int
	err := t.db.QueryRow(query).Scan(&count)
	return count, err
}

func (t *IssueTracker) RemoveIssue(issueURL string) error {
	query := `DELETE FROM tracked_issues WHERE issue_url = $1`
	result, err := t.db.Exec(query, issueURL)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("issue not found: %s", issueURL)
	}

	return nil
}

func (t *IssueTracker) IsTracking(issueURL string) (bool, error) {
	query := `SELECT 1 FROM tracked_issues WHERE issue_url = $1`
	var exists int
	err := t.db.QueryRow(query, issueURL).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (t *IssueTracker) MarkNotified(issueURL string) error {
	now := time.Now()
	query := `
	UPDATE tracked_issues 
	SET status = CASE WHEN status = 'new' THEN 'notified' ELSE status END,
	    notified_at = $1,
	    updated_at = $1
	WHERE issue_url = $2`

	_, err := t.db.Exec(query, now, issueURL)
	return err
}

func (t *IssueTracker) MarkAssignmentAsked(issueURL string) error {
	now := time.Now()
	query := `
	UPDATE tracked_issues 
	SET status = 'asked_assignment',
	    assignment_asked_at = $1,
	    updated_at = $1
	WHERE issue_url = $2`

	_, err := t.db.Exec(query, now, issueURL)
	return err
}

func (t *IssueTracker) WasNotified(issueURL string) (bool, error) {
	query := `SELECT 1 FROM tracked_issues WHERE issue_url = $1 AND notified_at IS NOT NULL`
	var exists int
	err := t.db.QueryRow(query, issueURL).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (t *IssueTracker) WasAskedAssignment(issueURL string) (bool, error) {
	query := `SELECT 1 FROM tracked_issues WHERE issue_url = $1 AND assignment_asked_at IS NOT NULL`
	var exists int
	err := t.db.QueryRow(query, issueURL).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (t *IssueTracker) GetAssignmentCandidates() ([]TrackedIssue, error) {
	query := `
	SELECT id, issue_url, issue_title, project_org, project_name, issue_number, 
	       status, notes, score, labels, created_at, updated_at, started_at, completed_at,
	       notified_at, assignment_asked_at, has_good_first, has_confirmed, has_assignee, has_pr
	FROM tracked_issues 
	WHERE has_good_first = TRUE 
	  AND has_confirmed = TRUE 
	  AND has_assignee = FALSE 
	  AND has_pr = FALSE
	  AND status = 'notified'
	ORDER BY score DESC`

	rows, err := t.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var issues []TrackedIssue
	for rows.Next() {
		var issue TrackedIssue
		err := rows.Scan(
			&issue.ID,
			&issue.IssueURL,
			&issue.IssueTitle,
			&issue.ProjectOrg,
			&issue.ProjectName,
			&issue.IssueNumber,
			&issue.Status,
			&issue.Notes,
			&issue.Score,
			&issue.Labels,
			&issue.CreatedAt,
			&issue.UpdatedAt,
			&issue.StartedAt,
			&issue.CompletedAt,
			&issue.NotifiedAt,
			&issue.AssignmentAskedAt,
			&issue.HasGoodFirst,
			&issue.HasConfirmed,
			&issue.HasAssignee,
			&issue.HasPR,
		)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}

	return issues, nil
}
