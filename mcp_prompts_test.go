package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPServer_RegisterPrompts(t *testing.T) {
	server := createTestMCPServer()
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "test-server",
		Version: "1.0.0",
	}, nil)

	server.RegisterPrompts(srv)
}

func TestMCPServer_HandleFindResumeWorthyIssuesPrompt(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()

	tests := []struct {
		name string
		args map[string]string
	}{
		{"no arguments", map[string]string{}},
		{"with min_stars", map[string]string{"min_stars": "5000"}},
		{"with category", map[string]string{"category": "kubernetes"}},
		{"with difficulty", map[string]string{"difficulty": "easy"}},
		{"all arguments", map[string]string{
			"min_stars":  "10000",
			"category":   "networking",
			"difficulty": "medium",
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &mcp.GetPromptRequest{
				Params: &mcp.GetPromptParams{
					Arguments: tt.args,
				},
			}

			result, err := server.handleFindResumeWorthyIssuesPrompt(ctx, req)

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Error("result should not be nil")
				return
			}

			if len(result.Messages) == 0 {
				t.Error("result should have at least one message")
				return
			}

			if result.Messages[0].Content == nil {
				t.Error("message content should not be nil")
			}
		})
	}
}

func TestMCPServer_HandleAnalyzeAndSuggestPrompt_MissingArgs(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()

	tests := []struct {
		name string
		args map[string]string
	}{
		{"missing owner", map[string]string{"repo": "repo", "issue_number": "123"}},
		{"missing repo", map[string]string{"owner": "owner", "issue_number": "123"}},
		{"missing issue_number", map[string]string{"owner": "owner", "repo": "repo"}},
		{"empty owner", map[string]string{"owner": "", "repo": "repo", "issue_number": "123"}},
		{"empty repo", map[string]string{"owner": "owner", "repo": "", "issue_number": "123"}},
		{"empty issue_number", map[string]string{"owner": "owner", "repo": "repo", "issue_number": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &mcp.GetPromptRequest{
				Params: &mcp.GetPromptParams{
					Arguments: tt.args,
				},
			}

			_, err := server.handleAnalyzeAndSuggestPrompt(ctx, req)

			if err == nil {
				t.Error("expected error for missing required argument")
			}
		})
	}
}

func TestMCPServer_HandleAnalyzeAndSuggestPrompt_ValidArgs(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()
	req := &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{
			Arguments: map[string]string{
				"owner":        "kubernetes",
				"repo":         "kubernetes",
				"issue_number": "12345",
			},
		},
	}

	result, err := server.handleAnalyzeAndSuggestPrompt(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if result == nil {
		t.Error("result should not be nil")
		return
	}

	if len(result.Messages) == 0 {
		t.Error("result should have at least one message")
		return
	}

	if result.Description == "" {
		t.Error("description should not be empty")
	}
}

func TestMCPServer_HandleCreateContributionPlanPrompt_MissingArgs(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()

	tests := []struct {
		name string
		args map[string]string
	}{
		{"missing owner", map[string]string{"repo": "repo", "issue_number": "123"}},
		{"missing repo", map[string]string{"owner": "owner", "issue_number": "123"}},
		{"missing issue_number", map[string]string{"owner": "owner", "repo": "repo"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &mcp.GetPromptRequest{
				Params: &mcp.GetPromptParams{
					Arguments: tt.args,
				},
			}

			_, err := server.handleCreateContributionPlanPrompt(ctx, req)

			if err == nil {
				t.Error("expected error for missing required argument")
			}
		})
	}
}

func TestMCPServer_HandleCreateContributionPlanPrompt_ValidArgs(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()
	req := &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{
			Arguments: map[string]string{
				"owner":        "kubernetes",
				"repo":         "kubernetes",
				"issue_number": "12345",
			},
		},
	}

	result, err := server.handleCreateContributionPlanPrompt(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if result == nil {
		t.Error("result should not be nil")
		return
	}

	if len(result.Messages) == 0 {
		t.Error("result should have at least one message")
	}
}

func TestMCPServer_HandleCreateContributionPlanPrompt_WithExperienceLevel(t *testing.T) {
	server := createTestMCPServer()

	tests := []struct {
		name            string
		experienceLevel string
	}{
		{"beginner", "beginner"},
		{"intermediate", "intermediate"},
		{"advanced", "advanced"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			req := &mcp.GetPromptRequest{
				Params: &mcp.GetPromptParams{
					Arguments: map[string]string{
						"owner":            "kubernetes",
						"repo":             "kubernetes",
						"issue_number":     "12345",
						"experience_level": tt.experienceLevel,
					},
				},
			}

			result, err := server.handleCreateContributionPlanPrompt(ctx, req)

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

func TestMCPServer_HandleGenerateIssueCommentPrompt_MissingArgs(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()

	tests := []struct {
		name string
		args map[string]string
	}{
		{"missing owner", map[string]string{"repo": "repo", "issue_number": "123"}},
		{"missing repo", map[string]string{"owner": "owner", "issue_number": "123"}},
		{"missing issue_number", map[string]string{"owner": "owner", "repo": "repo"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &mcp.GetPromptRequest{
				Params: &mcp.GetPromptParams{
					Arguments: tt.args,
				},
			}

			_, err := server.handleGenerateIssueCommentPrompt(ctx, req)

			if err == nil {
				t.Error("expected error for missing required argument")
			}
		})
	}
}

func TestMCPServer_HandleGenerateIssueCommentPrompt_ValidArgs(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()
	req := &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{
			Arguments: map[string]string{
				"owner":        "kubernetes",
				"repo":         "kubernetes",
				"issue_number": "12345",
			},
		},
	}

	result, err := server.handleGenerateIssueCommentPrompt(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if result == nil {
		t.Error("result should not be nil")
		return
	}

	if len(result.Messages) == 0 {
		t.Error("result should have at least one message")
	}
}

func TestMCPServer_HandleGenerateIssueCommentPrompt_WithCommentType(t *testing.T) {
	server := createTestMCPServer()

	tests := []struct {
		name        string
		commentType string
	}{
		{"interest", "interest"},
		{"assignment", "assignment"},
		{"clarification", "clarification"},
		{"solution", "solution"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			req := &mcp.GetPromptRequest{
				Params: &mcp.GetPromptParams{
					Arguments: map[string]string{
						"owner":        "kubernetes",
						"repo":         "kubernetes",
						"issue_number": "12345",
						"comment_type": tt.commentType,
					},
				},
			}

			result, err := server.handleGenerateIssueCommentPrompt(ctx, req)

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

func TestMCPServer_HandleGenerateIssueCommentPrompt_WithSkills(t *testing.T) {
	server := createTestMCPServer()

	ctx := context.Background()
	req := &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{
			Arguments: map[string]string{
				"owner":        "kubernetes",
				"repo":         "kubernetes",
				"issue_number": "12345",
				"comment_type": "interest",
				"skills":       "Go, Kubernetes, Docker",
			},
		},
	}

	result, err := server.handleGenerateIssueCommentPrompt(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if result == nil {
		t.Error("result should not be nil")
	}
}

func TestGetExperienceLevelGuidance(t *testing.T) {
	tests := []string{"beginner", "intermediate", "advanced", "unknown", "invalid"}

	for _, level := range tests {
		t.Run(level, func(t *testing.T) {
			result := getExperienceLevelGuidance(level)

			if len(result) == 0 {
				t.Error("guidance should not be empty")
			}
		})
	}
}

func TestGetCommentGuidance(t *testing.T) {
	tests := []string{"interest", "assignment", "clarification", "solution", "unknown"}

	for _, commentType := range tests {
		t.Run(commentType, func(t *testing.T) {
			result := getCommentGuidance(commentType)

			if result == "" {
				t.Error("guidance should not be empty")
			}
		})
	}
}

func TestGetSkillsPrompt(t *testing.T) {
	emptyResult := getSkillsPrompt("")
	if emptyResult == "" {
		t.Error("skills prompt should not be empty when skills is empty")
	}

	withSkills := getSkillsPrompt("Go, Kubernetes")
	if !containsString(withSkills, "Go, Kubernetes") {
		t.Error("skills prompt should contain skills")
	}
}

func TestGetCommentTypeInstructions(t *testing.T) {
	tests := []string{"interest", "assignment", "clarification", "solution", "unknown"}

	for _, commentType := range tests {
		t.Run(commentType, func(t *testing.T) {
			result := getCommentTypeInstructions(commentType)

			if result == "" {
				t.Error("instructions should not be empty")
			}
		})
	}
}
