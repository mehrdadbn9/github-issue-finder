package main

import (
	"testing"
	"time"
)

func TestValidateIssueDetails(t *testing.T) {
	tests := []struct {
		name    string
		details IssueDetails
		wantErr bool
	}{
		{
			name: "valid details",
			details: IssueDetails{
				Title:        "Test Issue",
				Body:         "Test body",
				URL:          "https://github.com/test/test/issues/1",
				Number:       1,
				ProjectOwner: "test",
				ProjectName:  "test",
			},
			wantErr: false,
		},
		{
			name: "missing title",
			details: IssueDetails{
				Title:        "",
				URL:          "https://github.com/test/test/issues/1",
				Number:       1,
				ProjectOwner: "test",
				ProjectName:  "test",
			},
			wantErr: true,
		},
		{
			name: "missing URL",
			details: IssueDetails{
				Title:        "Test",
				URL:          "",
				Number:       1,
				ProjectOwner: "test",
				ProjectName:  "test",
			},
			wantErr: true,
		},
		{
			name: "invalid issue number",
			details: IssueDetails{
				Title:        "Test",
				URL:          "https://github.com/test/test/issues/1",
				Number:       -1,
				ProjectOwner: "test",
				ProjectName:  "test",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateIssueDetails(tt.details)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateIssueDetails() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckIfIssueHasSolution(t *testing.T) {
	generator := NewSmartCommentGenerator()
	
	tests := []struct {
		name      string
		issueBody string
		wantSol   bool
	}{
		{
			name:      "issue with fixed in message",
			issueBody: "This was fixed in v1.2.3",
			wantSol:   true,
		},
		{
			name:      "issue with duplicate marker",
			issueBody: "This is a duplicate of #123",
			wantSol:   true,
		},
		{
			name:      "issue with won't fix",
			issueBody: "We've decided won't fix this issue",
			wantSol:   true,
		},
		{
			name:      "issue with workaround",
			issueBody: "workaround is to use the new API",
			wantSol:   true,
		},
		{
			name:      "open issue without solution",
			issueBody: "This is a problem we need to fix",
			wantSol:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := IssueDetails{
				Title:        "Test Issue",
				Body:         tt.issueBody,
				URL:          "https://github.com/test/test/issues/1",
				Number:       1,
				ProjectOwner: "test",
				ProjectName:  "test",
				CreatedAt:    time.Now(),
			}
			got := generator.CheckIfIssueHasSolution(details)
			if got != tt.wantSol {
				t.Errorf("CheckIfIssueHasSolution() = %v, want %v", got, tt.wantSol)
			}
		})
	}
}

func TestGenerateSmartCommentWithSolution(t *testing.T) {
	generator := NewSmartCommentGenerator()
	
	// Test that GenerateSmartComment returns error when solution exists
	details := IssueDetails{
		Title:        "Bug Report",
		Body:         "This issue was fixed in v2.0.0",
		URL:          "https://github.com/test/test/issues/1",
		Number:       1,
		ProjectOwner: "test",
		ProjectName:  "test",
		CreatedAt:    time.Now(),
	}
	
	comment, err := generator.GenerateSmartComment(details)
	if err == nil {
		t.Errorf("GenerateSmartComment() expected error for issue with solution, got %v", comment)
	}
}
