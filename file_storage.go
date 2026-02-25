package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type FileStorage struct {
	basePath string
	mu       sync.RWMutex
}

type FileHistory struct {
	Comments []FileCommentRecord `json:"comments"`
}

type FileDailyLimits struct {
	Date           string   `json:"date"`
	CommentsCount  int      `json:"comments_count"`
	ReposCommented []string `json:"repos_commented"`
}

type FileFoundIssues struct {
	Issues []FileFoundIssue `json:"issues"`
}

type FileFoundIssue struct {
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	Title       string    `json:"title"`
	Score       float64   `json:"score"`
	FoundAt     time.Time `json:"found_at"`
	Status      string    `json:"status"`
}

type FileCommentRecord struct {
	ID          int       `json:"id"`
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	IssueURL    string    `json:"issue_url"`
	CommentedAt time.Time `json:"commented_at"`
	CommentText string    `json:"comment_text"`
	Score       float64   `json:"score"`
}

type FileStatus struct {
	Enabled        bool      `json:"enabled"`
	AutoComment    bool      `json:"auto_comment"`
	TodayComments  int       `json:"today_comments"`
	TodayRepos     []string  `json:"today_repos"`
	LastUpdated    time.Time `json:"last_updated"`
	MaxCommentsDay int       `json:"max_comments_per_day"`
	PendingIssues  int       `json:"pending_issues"`
	LastCommentAt  time.Time `json:"last_comment_at"`
}

func NewFileStorage(basePath string) (*FileStorage, error) {
	if basePath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		basePath = filepath.Join(homeDir, ".github-issue-finder")
	}

	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}

	return &FileStorage{basePath: basePath}, nil
}

func (fs *FileStorage) historyPath() string {
	return filepath.Join(fs.basePath, "history.json")
}

func (fs *FileStorage) dailyLimitsPath() string {
	return filepath.Join(fs.basePath, "daily_limits.json")
}

func (fs *FileStorage) foundIssuesPath() string {
	return filepath.Join(fs.basePath, "found_issues.json")
}

func (fs *FileStorage) statusPath() string {
	return filepath.Join(fs.basePath, "status.json")
}

func (fs *FileStorage) configPath() string {
	return filepath.Join(fs.basePath, "config.json")
}

func (fs *FileStorage) LoadHistory() ([]FileCommentRecord, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	data, err := os.ReadFile(fs.historyPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []FileCommentRecord{}, nil
		}
		return nil, err
	}

	var history FileHistory
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, err
	}

	return history.Comments, nil
}

func (fs *FileStorage) SaveComment(comment FileCommentRecord) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	history, err := fs.LoadHistory()
	if err != nil {
		history = []FileCommentRecord{}
	}

	history = append(history, comment)

	data, err := json.MarshalIndent(FileHistory{Comments: history}, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(fs.historyPath(), data, 0644)
}

func (fs *FileStorage) LoadDailyLimits() (*FileDailyLimits, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	today := time.Now().Format("2006-01-02")
	data, err := os.ReadFile(fs.dailyLimitsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &FileDailyLimits{
				Date:           today,
				CommentsCount:  0,
				ReposCommented: []string{},
			}, nil
		}
		return nil, err
	}

	var limits FileDailyLimits
	if err := json.Unmarshal(data, &limits); err != nil {
		return nil, err
	}

	if limits.Date != today {
		return &FileDailyLimits{
			Date:           today,
			CommentsCount:  0,
			ReposCommented: []string{},
		}, nil
	}

	return &limits, nil
}

func (fs *FileStorage) SaveDailyLimits(limits *FileDailyLimits) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	data, err := json.MarshalIndent(limits, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(fs.dailyLimitsPath(), data, 0644)
}

func (fs *FileStorage) RecordComment(repo string) error {
	limits, err := fs.LoadDailyLimits()
	if err != nil {
		limits = &FileDailyLimits{
			Date:           time.Now().Format("2006-01-02"),
			CommentsCount:  0,
			ReposCommented: []string{},
		}
	}

	limits.CommentsCount++
	for _, r := range limits.ReposCommented {
		if r == repo {
			return nil
		}
	}
	limits.ReposCommented = append(limits.ReposCommented, repo)

	return fs.SaveDailyLimits(limits)
}

func (fs *FileStorage) CanCommentToday(maxComments int) bool {
	limits, err := fs.LoadDailyLimits()
	if err != nil {
		return true
	}
	return limits.CommentsCount < maxComments
}

func (fs *FileStorage) CanCommentOnRepo(repo string) bool {
	limits, err := fs.LoadDailyLimits()
	if err != nil {
		return true
	}
	for _, r := range limits.ReposCommented {
		if r == repo {
			return false
		}
	}
	return true
}

func (fs *FileStorage) LoadFoundIssues() ([]FileFoundIssue, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	data, err := os.ReadFile(fs.foundIssuesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []FileFoundIssue{}, nil
		}
		return nil, err
	}

	var issues FileFoundIssues
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, err
	}

	return issues.Issues, nil
}

func (fs *FileStorage) SaveFoundIssue(issue FileFoundIssue) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	issues, err := fs.LoadFoundIssues()
	if err != nil {
		issues = []FileFoundIssue{}
	}

	for i, existing := range issues {
		if existing.Repo == issue.Repo && existing.IssueNumber == issue.IssueNumber {
			issues[i] = issue
			data, err := json.MarshalIndent(FileFoundIssues{Issues: issues}, "", "  ")
			if err != nil {
				return err
			}
			return os.WriteFile(fs.foundIssuesPath(), data, 0644)
		}
	}

	issues = append(issues, issue)

	data, err := json.MarshalIndent(FileFoundIssues{Issues: issues}, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(fs.foundIssuesPath(), data, 0644)
}

func (fs *FileStorage) LoadStatus() (*FileStatus, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	data, err := os.ReadFile(fs.statusPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &FileStatus{
				Enabled:        false,
				AutoComment:    false,
				TodayComments:  0,
				TodayRepos:     []string{},
				MaxCommentsDay: 3,
				PendingIssues:  0,
			}, nil
		}
		return nil, err
	}

	var status FileStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, err
	}

	return &status, nil
}

func (fs *FileStorage) SaveStatus(status *FileStatus) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	status.LastUpdated = time.Now()

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(fs.statusPath(), data, 0644)
}

func (fs *FileStorage) GetTodayCommentsCount() (int, error) {
	limits, err := fs.LoadDailyLimits()
	if err != nil {
		return 0, err
	}
	return limits.CommentsCount, nil
}

func (fs *FileStorage) GetTodayRepos() ([]string, error) {
	limits, err := fs.LoadDailyLimits()
	if err != nil {
		return []string{}, err
	}
	return limits.ReposCommented, nil
}

func (fs *FileStorage) GetPendingIssuesCount(minScore float64) (int, error) {
	issues, err := fs.LoadFoundIssues()
	if err != nil {
		return 0, err
	}

	count := 0
	for _, issue := range issues {
		if issue.Score >= minScore && issue.Status == "found" {
			count++
		}
	}
	return count, nil
}

func (fs *FileStorage) ResetDaily() error {
	today := time.Now().Format("2006-01-02")
	return fs.SaveDailyLimits(&FileDailyLimits{
		Date:           today,
		CommentsCount:  0,
		ReposCommented: []string{},
	})
}

type FileWeeklyLimits struct {
	WeekStart     string `json:"week_start"`
	CommentsCount int    `json:"comments_count"`
}

func (fs *FileStorage) weeklyLimitsPath() string {
	return filepath.Join(fs.basePath, "weekly_limits.json")
}

func (fs *FileStorage) LoadWeeklyLimits() (*FileWeeklyLimits, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	data, err := os.ReadFile(fs.weeklyLimitsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &FileWeeklyLimits{
				WeekStart:     getWeekStart().Format("2006-01-02"),
				CommentsCount: 0,
			}, nil
		}
		return nil, err
	}

	var limits FileWeeklyLimits
	if err := json.Unmarshal(data, &limits); err != nil {
		return nil, err
	}

	return &limits, nil
}

func (fs *FileStorage) SaveWeeklyLimits(limits *FileWeeklyLimits) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	data, err := json.MarshalIndent(limits, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(fs.weeklyLimitsPath(), data, 0644)
}

func (fs *FileStorage) RecordWeeklyComment() error {
	limits, err := fs.LoadWeeklyLimits()
	if err != nil {
		limits = &FileWeeklyLimits{
			WeekStart:     getWeekStart().Format("2006-01-02"),
			CommentsCount: 0,
		}
	}

	currentWeek := getWeekStart().Format("2006-01-02")
	if limits.WeekStart != currentWeek {
		limits.WeekStart = currentWeek
		limits.CommentsCount = 0
	}

	limits.CommentsCount++
	return fs.SaveWeeklyLimits(limits)
}

func getWeekStart() time.Time {
	now := time.Now()
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	return now.AddDate(0, 0, -weekday+1).Truncate(24 * time.Hour)
}
