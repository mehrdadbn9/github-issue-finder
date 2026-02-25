package main

import (
	"testing"
	"time"

	"github.com/google/go-github/v58/github"
)

func TestEnhancedScorer_DescriptionQuality(t *testing.T) {
	scorer := NewEnhancedScorer()

	tests := []struct {
		name     string
		title    string
		body     string
		minScore float64
	}{
		{
			name:     "empty body",
			title:    "Bug fix needed",
			body:     "",
			minScore: 0.0,
		},
		{
			name:     "short body",
			title:    "Bug fix",
			body:     "Fix this",
			minScore: 0.0,
		},
		{
			name:     "good description with code",
			title:    "Fix bug in parser",
			body:     "This is a bug that needs fixing.\n\nSteps to reproduce:\n1. Run the code\n2. See error\n\nExpected: success\nActual: failure\n\n```go\nfunc main() {}\n```",
			minScore: 0.4,
		},
		{
			name:     "description with acceptance criteria",
			title:    "Add new feature",
			body:     "Add support for X.\n\nAcceptance criteria:\n- [ ] Implement feature\n- [ ] Add tests\n- [ ] Update docs",
			minScore: 0.1,
		},
		{
			name:     "clear scope",
			title:    "Fix bug in pkg/parser",
			body:     "File: pkg/parser/parse.go\nFunc: parseInput\nNeed to handle edge case",
			minScore: 0.1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &github.Issue{
				Title: github.String(tt.title),
				Body:  github.String(tt.body),
			}

			score := scorer.scoreDescriptionQuality(issue)
			if score < tt.minScore {
				t.Errorf("scoreDescriptionQuality() = %v, want at least %v", score, tt.minScore)
			}
		})
	}
}

func TestEnhancedScorer_ProjectActivity(t *testing.T) {
	scorer := NewEnhancedScorer()

	tests := []struct {
		name     string
		activity *RepoActivityInfo
		minScore float64
		maxScore float64
	}{
		{
			name:     "nil activity",
			activity: nil,
			minScore: 0.5,
			maxScore: 0.5,
		},
		{
			name: "very active project",
			activity: &RepoActivityInfo{
				LastCommit:          time.Now().Add(-1 * time.Hour),
				CommitsLastMonth:    100,
				PRActivityLastMonth: 50,
				OpenPRs:             20,
			},
			minScore: 0.7,
			maxScore: 1.0,
		},
		{
			name: "inactive project",
			activity: &RepoActivityInfo{
				LastCommit:          time.Now().Add(-90 * 24 * time.Hour),
				CommitsLastMonth:    0,
				PRActivityLastMonth: 0,
				OpenPRs:             0,
			},
			minScore: 0.0,
			maxScore: 0.3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := scorer.scoreProjectActivity(tt.activity)
			if score < tt.minScore || score > tt.maxScore {
				t.Errorf("scoreProjectActivity() = %v, want between %v and %v", score, tt.minScore, tt.maxScore)
			}
		})
	}
}

func TestEnhancedScorer_MaintainerResponsiveness(t *testing.T) {
	scorer := NewEnhancedScorer()

	tests := []struct {
		name     string
		activity *RepoActivityInfo
		minScore float64
	}{
		{
			name:     "nil activity",
			activity: nil,
			minScore: 0.5,
		},
		{
			name: "very responsive",
			activity: &RepoActivityInfo{
				AvgIssueResponseTime: 2 * time.Hour,
				PRReviewTime:         12 * time.Hour,
			},
			minScore: 0.6,
		},
		{
			name: "slow response",
			activity: &RepoActivityInfo{
				AvgIssueResponseTime: 7 * 24 * time.Hour,
				PRReviewTime:         14 * 24 * time.Hour,
			},
			minScore: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := scorer.scoreMaintainerResponsiveness(tt.activity)
			if score < tt.minScore {
				t.Errorf("scoreMaintainerResponsiveness() = %v, want at least %v", score, tt.minScore)
			}
		})
	}
}

func TestEnhancedScorer_BonusModifiers(t *testing.T) {
	scorer := NewEnhancedScorer()
	project := Project{Org: "kubernetes", Name: "kubernetes", Category: "Kubernetes", Stars: 100000}

	tests := []struct {
		name     string
		title    string
		body     string
		labels   []string
		minBonus float64
	}{
		{
			name:     "good first issue",
			title:    "Fix typo in docs",
			body:     "Simple typo fix",
			labels:   []string{"good first issue", "help wanted"},
			minBonus: 0.40,
		},
		{
			name:     "documentation issue",
			title:    "doc: Update README",
			body:     "Update documentation",
			labels:   []string{"documentation"},
			minBonus: 0.15,
		},
		{
			name:     "CNCF project bonus",
			title:    "Fix bug",
			body:     "Bug in kubernetes",
			labels:   []string{},
			minBonus: 0.10,
		},
		{
			name:     "TLS/Security bonus",
			title:    "Fix TLS issue",
			body:     "TLS certificate problem",
			labels:   []string{},
			minBonus: 0.10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ghLabels []*github.Label
			for _, l := range tt.labels {
				ghLabels = append(ghLabels, &github.Label{Name: github.String(l)})
			}

			issue := &github.Issue{
				Title:     github.String(tt.title),
				Body:      github.String(tt.body),
				Labels:    ghLabels,
				CreatedAt: &github.Timestamp{Time: time.Now()},
				Comments:  github.Int(0),
			}

			bonus := scorer.applyBonusModifiers(issue, project)
			if bonus < tt.minBonus {
				t.Errorf("applyBonusModifiers() = %v, want at least %v", bonus, tt.minBonus)
			}
		})
	}
}

func TestEnhancedScorer_PenaltyModifiers(t *testing.T) {
	scorer := NewEnhancedScorer()

	tests := []struct {
		name          string
		title         string
		body          string
		labels        []string
		initialScore  float64
		maxFinalScore float64
	}{
		{
			name:          "cloud provider penalty",
			title:         "Fix GKE integration",
			body:          "Problem with GCP and GKE",
			labels:        []string{},
			initialScore:  1.0,
			maxFinalScore: 0.5,
		},
		{
			name:          "needs triage penalty",
			title:         "New issue",
			body:          "Description",
			labels:        []string{"needs-triage"},
			initialScore:  1.0,
			maxFinalScore: 0.85,
		},
		{
			name:          "blocked issue penalty",
			title:         "Blocked feature",
			body:          "This is blocked by another PR",
			labels:        []string{},
			initialScore:  1.0,
			maxFinalScore: 0.8,
		},
		{
			name:          "wontfix penalty",
			title:         "Wontfix issue",
			body:          "Description",
			labels:        []string{"wontfix"},
			initialScore:  1.0,
			maxFinalScore: 0.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ghLabels []*github.Label
			for _, l := range tt.labels {
				ghLabels = append(ghLabels, &github.Label{Name: github.String(l)})
			}

			issue := &github.Issue{
				Title:     github.String(tt.title),
				Body:      github.String(tt.body),
				Labels:    ghLabels,
				CreatedAt: &github.Timestamp{Time: time.Now()},
				Comments:  github.Int(0),
			}

			finalScore := scorer.applyPenaltyModifiers(issue, tt.initialScore)
			if finalScore > tt.maxFinalScore {
				t.Errorf("applyPenaltyModifiers() = %v, want at most %v", finalScore, tt.maxFinalScore)
			}
		})
	}
}

func TestWorkStatus_String(t *testing.T) {
	statuses := []WorkStatus{
		StatusInterested,
		StatusAssigned,
		StatusInProgress,
		StatusCompleted,
		StatusAbandoned,
	}

	for _, status := range statuses {
		if string(status) == "" {
			t.Errorf("WorkStatus string representation should not be empty")
		}
	}
}

func TestNotificationSpamConfig_Default(t *testing.T) {
	config := DefaultNotificationSpamConfig()

	if config.MaxNotificationsPerHour <= 0 {
		t.Error("MaxNotificationsPerHour should be positive")
	}
	if config.DailyNotificationLimit <= 0 {
		t.Error("DailyNotificationLimit should be positive")
	}
	if config.NotificationCooldownPeriod <= 0 {
		t.Error("NotificationCooldownPeriod should be positive")
	}
}

func TestGetStatusEmoji(t *testing.T) {
	tests := []struct {
		status   WorkStatus
		expected string
	}{
		{StatusInterested, "👀"},
		{StatusAssigned, "📌"},
		{StatusInProgress, "🔧"},
		{StatusCompleted, "✅"},
		{StatusAbandoned, "❌"},
	}

	for _, tt := range tests {
		emoji := getStatusEmoji(tt.status)
		if emoji != tt.expected {
			t.Errorf("getStatusEmoji(%v) = %v, want %v", tt.status, emoji, tt.expected)
		}
	}
}

func TestParseIssueNumberFromURL(t *testing.T) {
	tests := []struct {
		url       string
		org       string
		repo      string
		number    int
		shouldErr bool
	}{
		{
			url:       "https://github.com/kubernetes/kubernetes/issues/123456",
			org:       "kubernetes",
			repo:      "kubernetes",
			number:    123456,
			shouldErr: false,
		},
		{
			url:       "https://github.com/golang/go/issues/77519",
			org:       "golang",
			repo:      "go",
			number:    77519,
			shouldErr: false,
		},
		{
			url:       "invalid-url",
			org:       "",
			repo:      "",
			number:    0,
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		org, repo, number, err := ParseIssueNumberFromURL(tt.url)
		if tt.shouldErr {
			if err == nil {
				t.Errorf("ParseIssueNumberFromURL(%v) should have returned error", tt.url)
			}
		} else {
			if err != nil {
				t.Errorf("ParseIssueNumberFromURL(%v) returned error: %v", tt.url, err)
			}
			if org != tt.org || repo != tt.repo || number != tt.number {
				t.Errorf("ParseIssueNumberFromURL(%v) = (%v, %v, %v), want (%v, %v, %v)",
					tt.url, org, repo, number, tt.org, tt.repo, tt.number)
			}
		}
	}
}
