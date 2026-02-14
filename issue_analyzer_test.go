package main

import (
	"testing"
)

func TestParseIssueURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantOwner  string
		wantRepo   string
		wantNumber int
		wantErr    bool
	}{
		{
			name:       "valid URL",
			url:        "https://github.com/golang/go/issues/12345",
			wantOwner:  "golang",
			wantRepo:   "go",
			wantNumber: 12345,
			wantErr:    false,
		},
		{
			name:    "invalid URL",
			url:     "https://example.com/not-an-issue",
			wantErr: true,
		},
		{
			name:    "PR URL",
			url:     "https://github.com/kubernetes/kubernetes/pull/12345",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, number, err := ParseIssueURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseIssueURL() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("ParseIssueURL() unexpected error: %v", err)
				return
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %v, want %v", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %v, want %v", repo, tt.wantRepo)
			}
			if number != tt.wantNumber {
				t.Errorf("number = %v, want %v", number, tt.wantNumber)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"this is a very long string", 10, "this is..."},
		{"exact", 5, "exact"},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

func TestHasTriageLabel(t *testing.T) {
	analyzer := &IssueAnalyzer{}

	tests := []struct {
		labels   []string
		expected bool
	}{
		{[]string{"needs-triage"}, true},
		{[]string{"NEEDS-TRIAGE"}, true},
		{[]string{"status/needs-triage"}, true},
		{[]string{"bug", "enhancement"}, false},
		{[]string{}, false},
	}

	for _, tt := range tests {
		result := analyzer.hasTriageLabel(tt.labels)
		if result != tt.expected {
			t.Errorf("hasTriageLabel(%v) = %v, want %v", tt.labels, result, tt.expected)
		}
	}
}

func TestGenerateCommentBody(t *testing.T) {
	tests := []struct {
		name    string
		labels  []string
		wantLen int
	}{
		{"good first issue", []string{"good first issue"}, 1},
		{"help wanted", []string{"help wanted"}, 1},
		{"no special labels", []string{"bug"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analysis := &IssueAnalysis{
				Title:  "Test issue",
				Labels: tt.labels,
			}
			body := generateCommentBody(analysis)
			if len(body) == 0 {
				t.Errorf("generateCommentBody() returned empty string")
			}
		})
	}
}

func TestIssueActionDetermination(t *testing.T) {
	tests := []struct {
		name           string
		analysis       *IssueAnalysis
		expectedAction IssueAction
	}{
		{
			name: "closed issue",
			analysis: &IssueAnalysis{
				State: "closed",
			},
			expectedAction: ActionSkipClosed,
		},
		{
			name: "has PR",
			analysis: &IssueAnalysis{
				State: "open",
				HasPR: true,
			},
			expectedAction: ActionSkipHasPR,
		},
		{
			name: "has assignee",
			analysis: &IssueAnalysis{
				State:       "open",
				HasAssignee: true,
				Assignees:   []string{"other-user"},
			},
			expectedAction: ActionSkipAssigned,
		},
		{
			name: "needs triage",
			analysis: &IssueAnalysis{
				State:       "open",
				NeedsTriage: true,
			},
			expectedAction: ActionSkipNeedsTriage,
		},
		{
			name: "ready for comment",
			analysis: &IssueAnalysis{
				State:            "open",
				UserHasCommented: false,
			},
			expectedAction: ActionComment,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.analysis.determineAction()
			if tt.analysis.Action != tt.expectedAction {
				t.Errorf("determineAction() = %v, want %v", tt.analysis.Action, tt.expectedAction)
			}
		})
	}
}
