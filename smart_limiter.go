package main

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

type SmartLimitsConfig struct {
	MaxPerRepoPerDay  int     `json:"max_per_repo_per_day" yaml:"max_per_repo_per_day"`
	BaseDailyLimit    int     `json:"base_daily_limit" yaml:"base_daily_limit"`
	MaxDailyLimit     int     `json:"max_daily_limit" yaml:"max_daily_limit"`
	MaxWeeklyLimit    int     `json:"max_weekly_limit" yaml:"max_weekly_limit"`
	QualityThreshold  float64 `json:"quality_threshold" yaml:"quality_threshold"`
	MinScoreToComment float64 `json:"min_score_to_comment" yaml:"min_score_to_comment"`
}

func DefaultSmartLimitsConfig() *SmartLimitsConfig {
	return &SmartLimitsConfig{
		MaxPerRepoPerDay:  1,
		BaseDailyLimit:    3,
		MaxDailyLimit:     7,
		MaxWeeklyLimit:    15,
		QualityThreshold:  0.85,
		MinScoreToComment: 0.70,
	}
}

type SmartLimiter struct {
	config        *SmartLimitsConfig
	todayComments int
	todayRepos    map[string]bool
	weekComments  int
	weekStart     time.Time
	mu            sync.RWMutex
	fileStorage   *FileStorage
	useDB         bool
	db            interface{}
}

func NewSmartLimiter(config *SmartLimitsConfig, fileStorage *FileStorage, db interface{}, useDB bool) *SmartLimiter {
	if config == nil {
		config = DefaultSmartLimitsConfig()
	}

	sl := &SmartLimiter{
		config:      config,
		todayRepos:  make(map[string]bool),
		fileStorage: fileStorage,
		db:          db,
		useDB:       useDB,
		weekStart:   getWeekStart(),
	}

	sl.loadState()
	return sl
}

func (sl *SmartLimiter) loadState() {
	if sl.fileStorage != nil {
		limits, err := sl.fileStorage.LoadDailyLimits()
		if err == nil {
			sl.todayComments = limits.CommentsCount
			for _, repo := range limits.ReposCommented {
				sl.todayRepos[repo] = true
			}
		}

		weekly, err := sl.fileStorage.LoadWeeklyLimits()
		if err == nil {
			if weekly.WeekStart == sl.weekStart.Format("2006-01-02") {
				sl.weekComments = weekly.CommentsCount
			}
		}
	}
}

func (sl *SmartLimiter) CanComment(repo string, score float64) (bool, string) {
	sl.mu.RLock()
	defer sl.mu.RUnlock()

	if sl.todayRepos[repo] {
		return false, "already commented on this repo today"
	}

	if sl.weekComments >= sl.config.MaxWeeklyLimit {
		return false, "weekly limit reached"
	}

	effectiveLimit := sl.config.BaseDailyLimit
	if score >= sl.config.QualityThreshold {
		effectiveLimit = sl.config.MaxDailyLimit
	}

	if sl.todayComments >= effectiveLimit {
		return false, fmt.Sprintf("daily limit (%d) reached", effectiveLimit)
	}

	if score < sl.config.MinScoreToComment {
		return false, fmt.Sprintf("score %.2f below minimum %.2f", score, sl.config.MinScoreToComment)
	}

	return true, "ok"
}

func (sl *SmartLimiter) RemainingToday(issues []ScoredIssue) int {
	sl.mu.RLock()
	defer sl.mu.RUnlock()

	uniqueRepos := make(map[string]bool)
	highQualityCount := 0

	for _, issue := range issues {
		repo := issue.Project.Name
		if !sl.todayRepos[repo] && !uniqueRepos[repo] {
			uniqueRepos[repo] = true
			if issue.Score.Total >= sl.config.QualityThreshold {
				highQualityCount++
			}
		}
	}

	if highQualityCount >= 3 {
		return sl.config.MaxDailyLimit - sl.todayComments
	}

	return sl.config.BaseDailyLimit - sl.todayComments
}

func (sl *SmartLimiter) RecordComment(repo string) error {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	sl.todayComments++
	sl.todayRepos[repo] = true
	sl.weekComments++

	if sl.fileStorage != nil {
		if err := sl.fileStorage.RecordComment(repo); err != nil {
			return err
		}
		return sl.fileStorage.RecordWeeklyComment()
	}

	return nil
}

func (sl *SmartLimiter) GetStatus() *SmartLimiterStatus {
	sl.mu.RLock()
	defer sl.mu.RUnlock()

	remaining := sl.config.BaseDailyLimit - sl.todayComments
	if remaining < 0 {
		remaining = 0
	}

	remainingWeekly := sl.config.MaxWeeklyLimit - sl.weekComments
	if remainingWeekly < 0 {
		remainingWeekly = 0
	}

	return &SmartLimiterStatus{
		TodayComments:    sl.todayComments,
		TodayRepos:       len(sl.todayRepos),
		WeekComments:     sl.weekComments,
		BaseLimit:        sl.config.BaseDailyLimit,
		MaxLimit:         sl.config.MaxDailyLimit,
		WeeklyLimit:      sl.config.MaxWeeklyLimit,
		RemainingToday:   remaining,
		RemainingWeekly:  remainingWeekly,
		QualityThreshold: sl.config.QualityThreshold,
		MinScore:         sl.config.MinScoreToComment,
		ReposCommented:   sl.todayRepos,
	}
}

type SmartLimiterStatus struct {
	TodayComments    int
	TodayRepos       int
	WeekComments     int
	BaseLimit        int
	MaxLimit         int
	WeeklyLimit      int
	RemainingToday   int
	RemainingWeekly  int
	QualityThreshold float64
	MinScore         float64
	ReposCommented   map[string]bool
}

type CommentStrategy struct {
	limiter *SmartLimiter
}

func NewCommentStrategy(limiter *SmartLimiter) *CommentStrategy {
	return &CommentStrategy{limiter: limiter}
}

func (cs *CommentStrategy) SelectIssuesToComment(issues []ScoredIssue) []ScoredIssue {
	var selected []ScoredIssue

	sort.Slice(issues, func(i, j int) bool {
		return issues[i].Score.Total > issues[j].Score.Total
	})

	reposUsed := make(map[string]bool)

	for _, issue := range issues {
		repo := issue.Project.Name
		if reposUsed[repo] {
			continue
		}

		canComment, _ := cs.limiter.CanComment(repo, issue.Score.Total)
		if !canComment {
			continue
		}

		if issue.Score.Total >= cs.limiter.config.MinScoreToComment {
			selected = append(selected, issue)
			reposUsed[repo] = true
		}

		if len(selected) >= cs.limiter.config.MaxDailyLimit {
			break
		}
	}

	return selected
}

type CommentPreview struct {
	Repo        string
	IssueNumber int
	Title       string
	Score       float64
	Comment     string
	URL         string
	Reason      string
}
