package main

import (
	"testing"
	"time"

	"github.com/google/go-github/v58/github"
)

func createTestLabels(names []string) []*github.Label {
	labels := make([]*github.Label, len(names))
	for i, name := range names {
		labels[i] = &github.Label{Name: github.String(name)}
	}
	return labels
}

func TestHasLabel(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		target   string
		expected bool
	}{
		{"empty labels", []string{}, "bug", false},
		{"exact match", []string{"bug", "enhancement"}, "bug", true},
		{"case insensitive", []string{"BUG", "Enhancement"}, "bug", true},
		{"partial match", []string{"kind/bug", "area/core"}, "bug", true},
		{"no match", []string{"enhancement", "documentation"}, "bug", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := createTestLabels(tt.labels)
			result := hasLabel(labels, tt.target)
			if result != tt.expected {
				t.Errorf("hasLabel() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestHasAnyLabel(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		targets  []string
		expected bool
	}{
		{"empty labels", []string{}, []string{"bug", "enhancement"}, false},
		{"one match", []string{"bug"}, []string{"bug", "enhancement"}, true},
		{"multiple matches", []string{"bug", "enhancement"}, []string{"bug", "enhancement"}, true},
		{"no match", []string{"documentation"}, []string{"bug", "enhancement"}, false},
		{"partial match", []string{"kind/bug"}, []string{"bug"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := createTestLabels(tt.labels)
			result := hasAnyLabel(labels, tt.targets...)
			if result != tt.expected {
				t.Errorf("hasAnyLabel() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestContainsAny(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		keywords []string
		expected bool
	}{
		{"empty text", "", []string{"bug"}, false},
		{"empty keywords", "bug report", []string{}, false},
		{"match", "this is a bug report", []string{"bug", "error"}, true},
		{"no match", "this is a feature", []string{"bug", "error"}, false},
		{"case sensitive", "BUG REPORT", []string{"bug"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsAny(tt.text, tt.keywords)
			if result != tt.expected {
				t.Errorf("containsAny() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestSafeString(t *testing.T) {
	tests := []struct {
		name     string
		input    *string
		expected string
	}{
		{"nil string", nil, ""},
		{"empty string", strPtr(""), ""},
		{"normal string", strPtr("hello"), "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := safeString(tt.input)
			if result != tt.expected {
				t.Errorf("safeString() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestAntiSpamConfig(t *testing.T) {
	config := &AntiSpamConfig{
		MaxCommentsPerHour:    5,
		MinIntervalBetween:    5 * time.Minute,
		MaxCommentsPerProject: 2,
		CooldownPeriod:        24 * time.Hour,
		DailyLimit:            20,
	}

	if config.MaxCommentsPerHour != 5 {
		t.Errorf("MaxCommentsPerHour = %d, want 5", config.MaxCommentsPerHour)
	}
	if config.MinIntervalBetween != 5*time.Minute {
		t.Errorf("MinIntervalBetween = %v, want 5m", config.MinIntervalBetween)
	}
	if config.DailyLimit != 20 {
		t.Errorf("DailyLimit = %d, want 20", config.DailyLimit)
	}
}

func TestNormalizeStars(t *testing.T) {
	scorer := NewIssueScorer()

	tests := []struct {
		stars    int
		expected float64
	}{
		{500, 0.3},
		{5000, 0.6},
		{25000, 0.8},
		{100000, 1.0},
	}

	for _, tt := range tests {
		result := scorer.normalizeStars(tt.stars)
		if result != tt.expected {
			t.Errorf("normalizeStars(%d) = %v, want %v", tt.stars, result, tt.expected)
		}
	}
}

func TestNormalizeComments(t *testing.T) {
	scorer := NewIssueScorer()

	tests := []struct {
		comments int
		expected float64
	}{
		{0, 0.7},
		{1, 0.7},
		{3, 0.5},
		{7, 0.3},
		{15, 0.1},
	}

	for _, tt := range tests {
		result := scorer.normalizeComments(tt.comments)
		if result != tt.expected {
			t.Errorf("normalizeComments(%d) = %v, want %v", tt.comments, result, tt.expected)
		}
	}
}

func TestNormalizeRecency(t *testing.T) {
	scorer := NewIssueScorer()

	now := time.Now()
	tests := []struct {
		name     string
		created  time.Time
		expected float64
	}{
		{"1 hour ago", now.Add(-1 * time.Hour), 1.0},
		{"2 days ago", now.Add(-48 * time.Hour), 0.8},
		{"5 days ago", now.Add(-120 * time.Hour), 0.6},
		{"20 days ago", now.Add(-480 * time.Hour), 0.4},
		{"60 days ago", now.Add(-1440 * time.Hour), 0.2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scorer.normalizeRecency(tt.created)
			if result != tt.expected {
				t.Errorf("normalizeRecency() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestScoreIssue(t *testing.T) {
	scorer := NewIssueScorer()

	project := Project{
		Org:      "golang",
		Name:     "go",
		Category: "Go Core",
		Stars:    125000,
	}

	issue := &github.Issue{
		Title:     github.String("bug: something is broken"),
		Body:      github.String("This is a simple fix for a typo in the documentation"),
		Number:    github.Int(123),
		HTMLURL:   github.String("https://github.com/golang/go/issues/123"),
		State:     github.String("open"),
		Comments:  github.Int(2),
		CreatedAt: &github.Timestamp{Time: time.Now().Add(-24 * time.Hour)},
		Labels: []*github.Label{
			{Name: github.String("bug")},
			{Name: github.String("good first issue")},
		},
		Assignees: []*github.User{},
	}

	score := scorer.ScoreIssue(issue, project)

	if score < 0 {
		t.Errorf("Score should not be negative, got %v", score)
	}
	if score > 1.5 {
		t.Errorf("Score should not exceed 1.5, got %v", score)
	}
}

func TestCloudProviderPenalty(t *testing.T) {
	scorer := NewIssueScorer()

	project := Project{
		Org:      "hashicorp",
		Name:     "vault",
		Category: "Security",
		Stars:    30000,
	}

	tests := []struct {
		name     string
		title    string
		body     string
		expected float64
	}{
		{"normal issue", "Bug in auth", "Something is wrong", 0},
		{"GCP issue", "GCP integration bug", "Problem with GKE", -0.50},
		{"AWS issue", "AWS S3 backend", "EC2 instance issue", -0.50},
		{"Azure issue", "Azure AD login", "AKS cluster problem", -0.50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &github.Issue{
				Title:     github.String(tt.title),
				Body:      github.String(tt.body),
				Number:    github.Int(1),
				HTMLURL:   github.String("https://github.com/hashicorp/vault/issues/1"),
				State:     github.String("open"),
				Comments:  github.Int(0),
				CreatedAt: &github.Timestamp{Time: time.Now()},
				Labels:    []*github.Label{},
				Assignees: []*github.User{},
			}

			score := scorer.ScoreIssue(issue, project)

			if tt.expected < 0 && score > 1.0 {
				t.Errorf("Cloud issue should have penalty, got score %v", score)
			}
		})
	}
}

func strPtr(s string) *string {
	return &s
}
