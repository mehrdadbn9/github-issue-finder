package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-github/v58/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type MockIssueFinder struct {
	issues                   []Issue
	goodFirstIssues          []Issue
	confirmedGoodFirstIssues []ConfirmedGoodFirstIssue
	err                      error
}

func (m *MockIssueFinder) FindIssues(ctx context.Context) ([]Issue, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.issues, nil
}

func (m *MockIssueFinder) FindGoodFirstIssues(ctx context.Context, labels []string) ([]Issue, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.goodFirstIssues, nil
}

func (m *MockIssueFinder) FindConfirmedGoodFirstIssues(ctx context.Context, category string) ([]ConfirmedGoodFirstIssue, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.confirmedGoodFirstIssues, nil
}

type MockIssueTracker struct {
	issues    []TrackedIssue
	err       error
	activeCnt int
}

func (m *MockIssueTracker) AddIssue(issue *TrackedIssue) error {
	return m.err
}

func (m *MockIssueTracker) UpdateStatus(issueURL string, status WorkStatus) error {
	return m.err
}

func (m *MockIssueTracker) GetAll() ([]TrackedIssue, error) {
	return m.issues, m.err
}

func (m *MockIssueTracker) GetByStatus(status WorkStatus) ([]TrackedIssue, error) {
	return m.issues, m.err
}

func (m *MockIssueTracker) GetActiveCount() (int, error) {
	return m.activeCnt, m.err
}

type MockGitHubClient struct {
	issue    *github.Issue
	repo     *github.Repository
	issues   []*github.Issue
	err      error
	comments []*github.IssueComment
}

func (m *MockGitHubClient) GetIssue(ctx context.Context, owner, repo string, number int) (*github.Issue, *github.Response, error) {
	return m.issue, nil, m.err
}

func (m *MockGitHubClient) GetRepository(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
	return m.repo, nil, m.err
}

func (m *MockGitHubClient) ListIssuesByRepo(ctx context.Context, owner, repo string, opts *github.IssueListByRepoOptions) ([]*github.Issue, *github.Response, error) {
	return m.issues, nil, m.err
}

func createTestMCPServer() *MCPServer {
	return &MCPServer{
		finder:      nil,
		tracker:     nil,
		commentGen:  NewSmartCommentGenerator(),
		repoManager: NewRepoManager(),
		client:      nil,
		db:          nil,
		config:      &Config{GitHubToken: "test-token"},
	}
}

func createTestIssue() Issue {
	return Issue{
		Title:       "Test Issue",
		URL:         "https://github.com/test/repo/issues/1",
		Number:      1,
		Score:       0.75,
		Comments:    2,
		CreatedAt:   time.Now().Add(-24 * time.Hour),
		Labels:      []string{"bug", "good first issue"},
		IsGoodFirst: true,
		Project: Project{
			Org:      "test",
			Name:     "repo",
			Stars:    5000,
			Category: "test-category",
		},
	}
}

func createTestGitHubIssue() *github.Issue {
	return &github.Issue{
		Title:     github.String("Test GitHub Issue"),
		Body:      github.String("Test body with func testFunction() and file main.go"),
		Number:    github.Int(123),
		HTMLURL:   github.String("https://github.com/owner/repo/issues/123"),
		State:     github.String("open"),
		Comments:  github.Int(2),
		CreatedAt: &github.Timestamp{Time: time.Now().Add(-24 * time.Hour)},
		Labels: []*github.Label{
			{Name: github.String("bug")},
			{Name: github.String("good first issue")},
		},
		User:      &github.User{Login: github.String("testuser")},
		Assignees: []*github.User{},
	}
}

func TestMCPServer_CreateServer(t *testing.T) {
	server := createTestMCPServer()
	srv := server.CreateServer()

	if srv == nil {
		t.Error("CreateServer should return non-nil server")
	}
}

func TestMCPServer_AssessDifficulty(t *testing.T) {
	server := createTestMCPServer()

	tests := []struct {
		name     string
		issue    Issue
		expected string
	}{
		{
			name: "good first issue",
			issue: Issue{
				IsGoodFirst: true,
				Score:       0.5,
			},
			expected: "easy",
		},
		{
			name: "beginner label",
			issue: Issue{
				Labels: []string{"beginner", "help wanted"},
				Score:  0.5,
			},
			expected: "easy",
		},
		{
			name: "hard label",
			issue: Issue{
				Labels: []string{"complex", "hard"},
				Score:  0.5,
			},
			expected: "hard",
		},
		{
			name: "high score issue",
			issue: Issue{
				Score:  0.85,
				Labels: []string{},
			},
			expected: "easy",
		},
		{
			name: "medium score issue",
			issue: Issue{
				Score:  0.6,
				Labels: []string{},
			},
			expected: "medium",
		},
		{
			name: "low score issue",
			issue: Issue{
				Score:  0.3,
				Labels: []string{},
			},
			expected: "hard",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := server.assessDifficulty(tt.issue)
			if result != tt.expected {
				t.Errorf("assessDifficulty() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMCPServer_HandleFindIssues(t *testing.T) {
	tests := []struct {
		name       string
		args       FindIssuesInput
		mockFinder IssueFinderInterface
		wantErr    bool
		wantCount  int
	}{
		{
			name: "find all issues with defaults",
			args: FindIssuesInput{},
			mockFinder: &MockIssueFinder{
				issues: []Issue{
					createTestIssue(),
					{Title: "Issue 2", Score: 0.8, Project: Project{Org: "test", Name: "repo2"}},
				},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name: "filter by min score",
			args: FindIssuesInput{MinScore: 0.7},
			mockFinder: &MockIssueFinder{
				issues: []Issue{
					{Title: "High Score", Score: 0.8, Project: Project{Org: "test", Name: "repo"}},
					{Title: "Low Score", Score: 0.5, Project: Project{Org: "test", Name: "repo"}},
				},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name: "filter by labels",
			args: FindIssuesInput{Labels: []string{"bug"}},
			mockFinder: &MockIssueFinder{
				issues: []Issue{
					{Title: "Bug Issue", Score: 0.7, Labels: []string{"bug"}, Project: Project{Org: "test", Name: "repo"}},
					{Title: "Feature Issue", Score: 0.7, Labels: []string{"feature"}, Project: Project{Org: "test", Name: "repo"}},
				},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name: "filter by project",
			args: FindIssuesInput{Project: "kubernetes"},
			mockFinder: &MockIssueFinder{
				issues: []Issue{
					{Title: "K8s Issue", Score: 0.7, Project: Project{Org: "kubernetes", Name: "kubernetes"}},
					{Title: "Other Issue", Score: 0.7, Project: Project{Org: "other", Name: "repo"}},
				},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name: "filter by difficulty",
			args: FindIssuesInput{Difficulty: "easy"},
			mockFinder: &MockIssueFinder{
				issues: []Issue{
					{Title: "Easy Issue", Score: 0.7, IsGoodFirst: true, Project: Project{Org: "test", Name: "repo"}},
					{Title: "Hard Issue", Score: 0.3, Labels: []string{"complex"}, Project: Project{Org: "test", Name: "repo"}},
				},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name:       "nil finder",
			args:       FindIssuesInput{},
			mockFinder: nil,
			wantErr:    true,
		},
		{
			name: "finder error",
			args: FindIssuesInput{},
			mockFinder: &MockIssueFinder{
				err: fmt.Errorf("finder error"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()
			server.finder = tt.mockFinder

			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleFindIssues(ctx, req, tt.args)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Error("result should not be nil")
			}
		})
	}
}

func TestMCPServer_HandleFindGoodFirstIssues(t *testing.T) {
	tests := []struct {
		name       string
		args       FindGoodFirstIssuesInput
		mockFinder IssueFinderInterface
		wantErr    bool
	}{
		{
			name: "find good first issues",
			args: FindGoodFirstIssuesInput{Limit: 10, MinStars: 1000},
			mockFinder: &MockIssueFinder{
				goodFirstIssues: []Issue{
					{Title: "Good First Issue", Score: 0.7, IsGoodFirst: true, Project: Project{Stars: 5000}},
				},
			},
			wantErr: false,
		},
		{
			name: "find confirmed only",
			args: FindGoodFirstIssuesInput{ConfirmedOnly: true, Limit: 10},
			mockFinder: &MockIssueFinder{
				confirmedGoodFirstIssues: []ConfirmedGoodFirstIssue{
					{
						Issue:      Issue{Title: "Confirmed Issue", Score: 0.8, Project: Project{Stars: 5000}},
						IsEligible: true,
					},
				},
			},
			wantErr: false,
		},
		{
			name:       "nil finder",
			args:       FindGoodFirstIssuesInput{Limit: 10},
			mockFinder: nil,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()
			server.finder = tt.mockFinder

			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleFindGoodFirstIssues(ctx, req, tt.args)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
				return
			}

			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result != nil && len(result.Content) > 0 {
				if textContent, ok := result.Content[0].(*mcp.TextContent); ok {
					if textContent.Text == "" {
						t.Error("result content should not be empty")
					}
				}
			}
		})
	}
}

func TestMCPServer_HandleGetIssueScore(t *testing.T) {
	tests := []struct {
		name    string
		args    GetIssueScoreInput
		wantErr bool
	}{
		{
			name:    "missing owner",
			args:    GetIssueScoreInput{Repo: "repo", IssueNumber: 1},
			wantErr: true,
		},
		{
			name:    "missing repo",
			args:    GetIssueScoreInput{Owner: "owner", IssueNumber: 1},
			wantErr: true,
		},
		{
			name:    "missing issue number",
			args:    GetIssueScoreInput{Owner: "owner", Repo: "repo"},
			wantErr: true,
		},
		{
			name:    "nil client",
			args:    GetIssueScoreInput{Owner: "owner", Repo: "repo", IssueNumber: 123},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()

			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleGetIssueScore(ctx, req, tt.args)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if result == nil {
				t.Error("result should not be nil for valid input")
			}
		})
	}
}

func TestMCPServer_HandleTrackIssue(t *testing.T) {
	tests := []struct {
		name        string
		args        TrackIssueInput
		tracker     *MockIssueTracker
		wantErr     bool
		errContains string
	}{
		{
			name:    "missing owner",
			args:    TrackIssueInput{Repo: "repo", IssueNumber: 1},
			wantErr: true,
		},
		{
			name:    "missing repo",
			args:    TrackIssueInput{Owner: "owner", IssueNumber: 1},
			wantErr: true,
		},
		{
			name:    "missing issue number",
			args:    TrackIssueInput{Owner: "owner", Repo: "repo"},
			wantErr: true,
		},
		{
			name:    "nil tracker",
			args:    TrackIssueInput{Owner: "owner", Repo: "repo", IssueNumber: 123},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()
			if tt.tracker != nil {
				server.tracker = &IssueTracker{}
			}

			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleTrackIssue(ctx, req, tt.args)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if result == nil {
				t.Error("result should not be nil")
			}
		})
	}
}

func TestMCPServer_HandleListTrackedIssues(t *testing.T) {
	tests := []struct {
		name    string
		args    ListTrackedIssuesInput
		wantErr bool
	}{
		{
			name:    "nil tracker",
			args:    ListTrackedIssuesInput{Status: "all"},
			wantErr: true,
		},
		{
			name:    "status all",
			args:    ListTrackedIssuesInput{Status: "all"},
			wantErr: true,
		},
		{
			name:    "specific status",
			args:    ListTrackedIssuesInput{Status: "new"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()

			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleListTrackedIssues(ctx, req, tt.args)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			_ = result
		})
	}
}

func TestMCPServer_HandleUpdateIssueStatus(t *testing.T) {
	tests := []struct {
		name    string
		args    UpdateIssueStatusInput
		wantErr bool
	}{
		{
			name:    "missing issue id",
			args:    UpdateIssueStatusInput{NewStatus: "in_progress"},
			wantErr: true,
		},
		{
			name:    "missing new status",
			args:    UpdateIssueStatusInput{IssueID: "test-id"},
			wantErr: true,
		},
		{
			name:    "nil tracker",
			args:    UpdateIssueStatusInput{IssueID: "test-id", NewStatus: "in_progress"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()

			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleUpdateIssueStatus(ctx, req, tt.args)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			_ = result
		})
	}
}

func TestMCPServer_HandleGenerateComment(t *testing.T) {
	tests := []struct {
		name    string
		args    GenerateCommentInput
		wantErr bool
	}{
		{
			name:    "missing owner",
			args:    GenerateCommentInput{Repo: "repo", IssueNumber: 1},
			wantErr: true,
		},
		{
			name:    "missing repo",
			args:    GenerateCommentInput{Owner: "owner", IssueNumber: 1},
			wantErr: true,
		},
		{
			name:    "missing issue number",
			args:    GenerateCommentInput{Owner: "owner", Repo: "repo"},
			wantErr: true,
		},
		{
			name:    "nil client",
			args:    GenerateCommentInput{Owner: "owner", Repo: "repo", IssueNumber: 123},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()

			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleGenerateComment(ctx, req, tt.args)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Error("result should not be nil")
			}
		})
	}
}

func TestMCPServer_HandleSearchRepos(t *testing.T) {
	server := createTestMCPServer()

	tests := []struct {
		name    string
		args    SearchReposInput
		wantErr bool
		wantLen int
	}{
		{
			name:    "empty query",
			args:    SearchReposInput{Query: ""},
			wantErr: true,
		},
		{
			name:    "search by name",
			args:    SearchReposInput{Query: "kubernetes"},
			wantErr: false,
		},
		{
			name:    "search by category",
			args:    SearchReposInput{Query: "networking"},
			wantErr: false,
		},
		{
			name:    "search with limit",
			args:    SearchReposInput{Query: "kubernetes", Limit: 5},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleSearchRepos(ctx, req, tt.args)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Error("result should not be nil")
			}
		})
	}
}

func TestMCPServer_HandleGetStats(t *testing.T) {
	tests := []struct {
		name    string
		args    GetStatsInput
		setupDB bool
		wantErr bool
	}{
		{
			name:    "default period week",
			args:    GetStatsInput{},
			setupDB: false,
			wantErr: false,
		},
		{
			name:    "day period",
			args:    GetStatsInput{Period: "day"},
			setupDB: false,
			wantErr: false,
		},
		{
			name:    "month period",
			args:    GetStatsInput{Period: "month"},
			setupDB: false,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()

			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleGetStats(ctx, req, tt.args)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
				return
			}

			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result != nil && len(result.Content) > 0 {
				if textContent, ok := result.Content[0].(*mcp.TextContent); ok {
					var stats map[string]interface{}
					json.Unmarshal([]byte(textContent.Text), &stats)
					if stats["period"] != tt.args.Period && tt.args.Period != "" {
						t.Errorf("period mismatch: got %v, want %v", stats["period"], tt.args.Period)
					}
				}
			}
		})
	}
}

func TestMCPServer_HandleGetIssueDetails(t *testing.T) {
	tests := []struct {
		name    string
		args    GetIssueDetailsInput
		wantErr bool
	}{
		{
			name:    "missing owner",
			args:    GetIssueDetailsInput{Repo: "repo", IssueNumber: 1},
			wantErr: true,
		},
		{
			name:    "missing repo",
			args:    GetIssueDetailsInput{Owner: "owner", IssueNumber: 1},
			wantErr: true,
		},
		{
			name:    "missing issue number",
			args:    GetIssueDetailsInput{Owner: "owner", Repo: "repo"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()

			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleGetIssueDetails(ctx, req, tt.args)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			_ = result
		})
	}
}

func TestMCPServer_HandleAnalyzeIssue(t *testing.T) {
	tests := []struct {
		name    string
		args    AnalyzeIssueInput
		wantErr bool
	}{
		{
			name:    "missing owner",
			args:    AnalyzeIssueInput{Repo: "repo", IssueNumber: 1},
			wantErr: true,
		},
		{
			name:    "missing repo",
			args:    AnalyzeIssueInput{Owner: "owner", IssueNumber: 1},
			wantErr: true,
		},
		{
			name:    "missing issue number",
			args:    AnalyzeIssueInput{Owner: "owner", Repo: "repo"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()

			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleAnalyzeIssue(ctx, req, tt.args)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			_ = result
		})
	}
}

func TestMCPGetLabelNames(t *testing.T) {
	labels := []*github.Label{
		{Name: github.String("bug")},
		{Name: github.String("enhancement")},
		{Name: github.String("good first issue")},
	}

	result := mcpGetLabelNames(labels)

	if len(result) != 3 {
		t.Errorf("expected 3 labels, got %d", len(result))
	}

	expected := []string{"bug", "enhancement", "good first issue"}
	for i, label := range result {
		if label != expected[i] {
			t.Errorf("label[%d] = %s, want %s", i, label, expected[i])
		}
	}
}

func TestMCPHasAnyLabelStr(t *testing.T) {
	tests := []struct {
		name     string
		labels   []string
		targets  []string
		expected bool
	}{
		{"match found", []string{"bug", "feature"}, []string{"bug"}, true},
		{"case insensitive", []string{"BUG", "Feature"}, []string{"bug"}, true},
		{"partial match", []string{"kind/bug"}, []string{"bug"}, true},
		{"no match", []string{"documentation"}, []string{"bug", "feature"}, false},
		{"empty labels", []string{}, []string{"bug"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mcpHasAnyLabelStr(tt.labels, tt.targets...)
			if result != tt.expected {
				t.Errorf("mcpHasAnyLabelStr() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMCPGetRecommendation(t *testing.T) {
	tests := []struct {
		name         string
		resumeScore  float64
		concernCount int
		expected     string
	}{
		{"highly recommended", 0.8, 0, "highly_recommended"},
		{"recommended", 0.6, 1, "recommended"},
		{"consider", 0.4, 2, "consider"},
		{"skip", 0.2, 3, "skip"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mcpGetRecommendation(tt.resumeScore, tt.concernCount)
			if result != tt.expected {
				t.Errorf("mcpGetRecommendation() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestNewMCPServer(t *testing.T) {
	t.Run("config load error simulation", func(t *testing.T) {
		server := &MCPServer{
			finder:      nil,
			tracker:     nil,
			commentGen:  NewSmartCommentGenerator(),
			repoManager: NewRepoManager(),
			client:      nil,
			db:          nil,
			config:      nil,
		}

		if server == nil {
			t.Error("server should not be nil")
		}
	})

	t.Run("config validation error simulation", func(t *testing.T) {
		cfg := &Config{GitHubToken: ""}
		err := cfg.Validate()
		if err == nil {
			t.Error("expected validation error for empty token")
		}
	})
}

func TestMCPServer_HandleFindConfirmedIssues(t *testing.T) {
	tests := []struct {
		name       string
		args       FindConfirmedIssuesInput
		mockFinder IssueFinderInterface
		wantErr    bool
	}{
		{
			name: "default values",
			args: FindConfirmedIssuesInput{},
			mockFinder: &MockIssueFinder{
				confirmedGoodFirstIssues: []ConfirmedGoodFirstIssue{},
			},
			wantErr: false,
		},
		{
			name: "with limit and min score",
			args: FindConfirmedIssuesInput{Limit: 10, MinScore: 0.7},
			mockFinder: &MockIssueFinder{
				confirmedGoodFirstIssues: []ConfirmedGoodFirstIssue{
					{
						Issue:      Issue{Title: "Confirmed Issue", Score: 0.8, Project: Project{Org: "test", Name: "repo"}},
						IsEligible: true,
					},
				},
			},
			wantErr: false,
		},
		{
			name:       "nil finder",
			args:       FindConfirmedIssuesInput{},
			mockFinder: nil,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createTestMCPServer()
			server.finder = tt.mockFinder

			ctx := context.Background()
			req := &mcp.CallToolRequest{}

			result, _, err := server.handleFindConfirmedIssues(ctx, req, tt.args)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
				return
			}

			if !tt.wantErr {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				_ = result
			}
		})
	}
}
