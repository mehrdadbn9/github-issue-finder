package main

import (
	"testing"
	"time"
)

func TestNewAIEnhancer(t *testing.T) {
	tests := []struct {
		name      string
		mcpClient *MCPClient
	}{
		{"nil client", nil},
		{"with client", NewMCPClient(nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enhancer := NewAIEnhancer(tt.mcpClient)

			if enhancer == nil {
				t.Error("NewAIEnhancer should return non-nil enhancer")
			}
		})
	}
}

func TestAIEnhancer_SetFallbackEnabled(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	enhancer.SetFallbackEnabled(true)
	if !enhancer.fallbackEnabled {
		t.Error("fallbackEnabled should be true")
	}

	enhancer.SetFallbackEnabled(false)
	if enhancer.fallbackEnabled {
		t.Error("fallbackEnabled should be false")
	}
}

func TestAIEnhancer_EnhanceComment_NilClient(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	details := IssueDetails{
		Title:        "Test Issue",
		Body:         "Test body",
		Labels:       []string{"bug"},
		Number:       123,
		ProjectOwner: "owner",
		ProjectName:  "repo",
	}

	result := enhancer.EnhanceComment(details, "draft comment")

	if result == nil {
		t.Error("EnhanceComment should return non-nil result")
		return
	}

	if result.AIGenerated {
		t.Error("result should not be AI generated with nil client")
	}

	if result.OriginalBody != "draft comment" {
		t.Errorf("OriginalBody = %v, want draft comment", result.OriginalBody)
	}
}

func TestAIEnhancer_SummarizeIssue_NilClient(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	details := IssueDetails{
		Title:        "Test Issue",
		Body:         "Test body with func testFunction()",
		Labels:       []string{"bug"},
		Number:       123,
		ProjectOwner: "owner",
		ProjectName:  "repo",
		Comments:     5,
		HasAssignee:  true,
		HasLinkedPR:  true,
	}

	result := enhancer.SummarizeIssue(details)

	if result == nil {
		t.Error("SummarizeIssue should return non-nil result")
		return
	}

	if result.Summary == "" {
		t.Error("summary should not be empty")
	}
}

func TestAIEnhancer_SuggestApproach_NilClient(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	details := IssueDetails{
		Title:        "Bug in testFunction",
		Body:         "The testFunction crashes. File main.go line 42",
		Labels:       []string{"bug"},
		Number:       123,
		ProjectOwner: "owner",
		ProjectName:  "repo",
	}

	result := enhancer.SuggestApproach(details)

	if result == nil {
		t.Error("SuggestApproach should return non-nil result")
		return
	}

	if result.Approach == "" {
		t.Error("approach should not be empty")
	}
}

func TestAIEnhancer_AnalyzeDifficulty_NilClient(t *testing.T) {
	tests := []struct {
		name        string
		details     IssueDetails
		wantLevel   string
		wantScoreGt float64
	}{
		{
			name: "good first issue",
			details: IssueDetails{
				Title:  "Good first issue",
				Labels: []string{"good first issue"},
			},
			wantLevel:   "easy",
			wantScoreGt: 0.0,
		},
		{
			name: "bug issue",
			details: IssueDetails{
				Title:  "Bug in system",
				Labels: []string{"bug"},
			},
			wantLevel:   "medium",
			wantScoreGt: 0.0,
		},
		{
			name: "feature request",
			details: IssueDetails{
				Title:  "Add new feature",
				Labels: []string{"feature", "enhancement"},
			},
			wantLevel:   "hard",
			wantScoreGt: 0.0,
		},
		{
			name: "documentation issue",
			details: IssueDetails{
				Title:  "Update documentation",
				Labels: []string{"documentation"},
			},
			wantLevel:   "easy",
			wantScoreGt: 0.0,
		},
		{
			name: "security issue",
			details: IssueDetails{
				Title:  "Security vulnerability",
				Labels: []string{"security"},
			},
			wantLevel:   "hard",
			wantScoreGt: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enhancer := NewAIEnhancer(nil)

			result := enhancer.AnalyzeDifficulty(tt.details)

			if result == nil {
				t.Error("AnalyzeDifficulty should return non-nil result")
				return
			}

			if result.Level != tt.wantLevel {
				t.Errorf("Level = %v, want %v", result.Level, tt.wantLevel)
			}

			if result.Score <= tt.wantScoreGt {
				t.Errorf("Score should be > %v", tt.wantScoreGt)
			}
		})
	}
}

func TestAIEnhancer_FallbackEnhanceComment(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	details := IssueDetails{
		Title:        "Bug in HandleRequest",
		Body:         "The HandleRequest function crashes",
		Labels:       []string{"bug"},
		Number:       123,
		ProjectOwner: "owner",
		ProjectName:  "repo",
	}

	result := enhancer.fallbackEnhanceComment(details, "test comment")

	if result == nil {
		t.Error("fallbackEnhanceComment should return non-nil result")
		return
	}

	if result.AIGenerated {
		t.Error("fallback should not be AI generated")
	}
}

func TestAIEnhancer_FallbackSummarizeIssue(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	details := IssueDetails{
		Title:       "Bug issue",
		Body:        "Test body",
		Labels:      []string{"bug"},
		HasAssignee: false,
		HasLinkedPR: false,
		Comments:    0,
	}

	result := enhancer.fallbackSummarizeIssue(details)

	if result == nil {
		t.Error("fallbackSummarizeIssue should return non-nil result")
		return
	}

	if result.Summary == "" {
		t.Error("summary should not be empty")
	}
}

func TestAIEnhancer_FallbackSuggestApproach(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	details := IssueDetails{
		Title: "Bug with files",
		Body:  "File main.go line 42 has an error",
	}

	result := enhancer.fallbackSuggestApproach(details)

	if result == nil {
		t.Error("fallbackSuggestApproach should return non-nil result")
		return
	}

	if result.Approach == "" {
		t.Error("approach should not be empty")
	}
}

func TestAIEnhancer_FallbackAnalyzeDifficulty(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	details := IssueDetails{
		Title:  "Bug in system",
		Labels: []string{"bug"},
	}

	result := enhancer.fallbackAnalyzeDifficulty(details)

	if result == nil {
		t.Error("fallbackAnalyzeDifficulty should return non-nil result")
		return
	}

	if result.Level == "" {
		t.Error("level should not be empty")
	}
}

func TestAIEnhancer_CalculateQualityScore(t *testing.T) {
	details := IssueDetails{
		Title: "Bug in HandleRequest function",
	}

	enhancer := NewAIEnhancer(nil)

	tests := []string{
		"short",
		"This is a longer comment with more context about the issue",
		"Comment with `code` formatting",
		"Comment with a question?",
		"This comment mentions HandleRequest",
	}

	for _, comment := range tests {
		t.Run(comment, func(t *testing.T) {
			result := enhancer.calculateQualityScore(comment, details)

			if result < 0 || result > 1 {
				t.Errorf("score should be between 0 and 1, got %v", result)
			}
		})
	}
}

func TestAIEnhancer_EstimateTime(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	tests := []struct {
		level    string
		expected string
	}{
		{"easy", "1-2 hours"},
		{"medium", "2-4 hours"},
		{"hard", "1-2 days"},
		{"unknown", "varies"},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			result := enhancer.estimateTime(tt.level)

			if result != tt.expected {
				t.Errorf("estimateTime(%s) = %v, want %v", tt.level, result, tt.expected)
			}
		})
	}
}

func TestAIEnhancer_GetStats(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	stats := enhancer.GetStats()

	if stats.TotalRequests != 0 {
		t.Errorf("initial TotalRequests should be 0, got %d", stats.TotalRequests)
	}
}

func TestAIEnhancer_ResetStats(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	enhancer.stats.TotalRequests = 100
	enhancer.stats.SuccessfulRequests = 50

	enhancer.ResetStats()

	stats := enhancer.GetStats()

	if stats.TotalRequests != 0 {
		t.Errorf("TotalRequests should be 0 after reset, got %d", stats.TotalRequests)
	}

	if stats.SuccessfulRequests != 0 {
		t.Errorf("SuccessfulRequests should be 0 after reset, got %d", stats.SuccessfulRequests)
	}
}

func TestAIEnhancer_IsAvailable(t *testing.T) {
	tests := []struct {
		name      string
		mcpClient *MCPClient
		expected  bool
	}{
		{"nil client", nil, false},
		{"with client", NewMCPClient(nil), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enhancer := NewAIEnhancer(tt.mcpClient)

			result := enhancer.IsAvailable()

			if result != tt.expected {
				t.Errorf("IsAvailable() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestAIEnhancer_ExtractKeyPointsFromText(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	tests := []struct {
		name   string
		text   string
		wantGt int
	}{
		{"empty text", "", 0},
		{"short text", "short", 0},
		{"bullet points", "- Point one\n- Point two\n- Point three", 3},
		{"numbered list", "1. First item\n2. Second item", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := enhancer.extractKeyPointsFromText(tt.text)

			if len(result) < tt.wantGt {
				t.Errorf("extractKeyPointsFromText returned %d points, want >= %d", len(result), tt.wantGt)
			}
		})
	}
}

func TestAIEnhancer_ExtractStepsFromText(t *testing.T) {
	enhancer := NewAIEnhancer(nil)

	tests := []struct {
		name   string
		text   string
		wantGt int
	}{
		{"empty text", "", 0},
		{"bullet steps", "- Step one\n- Step two\n- Step three", 3},
		{"numbered steps", "1. First step\n2. Second step\n3. Third step", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := enhancer.extractStepsFromText(tt.text)

			if len(result) < tt.wantGt {
				t.Errorf("extractStepsFromText returned %d steps, want >= %d", len(result), tt.wantGt)
			}
		})
	}
}

func TestEnhancedComment(t *testing.T) {
	comment := &EnhancedComment{
		Body:           "Enhanced comment body",
		OriginalBody:   "Original comment",
		Enhancements:   []string{"Added context", "Improved clarity"},
		QualityScore:   0.85,
		AIGenerated:    true,
		ServerUsed:     "test-server",
		GenerationTime: 100 * time.Millisecond,
	}

	if comment.Body != "Enhanced comment body" {
		t.Errorf("Body = %v, want Enhanced comment body", comment.Body)
	}

	if !comment.AIGenerated {
		t.Error("AIGenerated should be true")
	}

	if comment.QualityScore < 0 || comment.QualityScore > 1 {
		t.Errorf("QualityScore should be between 0 and 1, got %v", comment.QualityScore)
	}
}

func TestIssueSummary(t *testing.T) {
	summary := &IssueSummary{
		Summary:       "Issue summary text",
		KeyPoints:     []string{"Point 1", "Point 2"},
		SuggestedArea: "backend",
		Difficulty:    "medium",
		Confidence:    0.75,
		ServerUsed:    "test-server",
	}

	if summary.Summary != "Issue summary text" {
		t.Errorf("Summary = %v, want Issue summary text", summary.Summary)
	}

	if len(summary.KeyPoints) != 2 {
		t.Errorf("KeyPoints count = %d, want 2", len(summary.KeyPoints))
	}
}

func TestSolutionSuggestion(t *testing.T) {
	suggestion := &SolutionSuggestion{
		Approach:      "Fix the bug by adding null check",
		Steps:         []string{"Add null check", "Write test", "Submit PR"},
		FilesToModify: []string{"main.go", "main_test.go"},
		Risks:         []string{"Breaking existing behavior"},
		Confidence:    0.8,
		ServerUsed:    "test-server",
	}

	if suggestion.Approach == "" {
		t.Error("Approach should not be empty")
	}

	if len(suggestion.Steps) != 3 {
		t.Errorf("Steps count = %d, want 3", len(suggestion.Steps))
	}
}

func TestDifficultyAssessment(t *testing.T) {
	assessment := &DifficultyAssessment{
		Level:        "medium",
		Score:        0.5,
		Reasons:      []string{"Requires debugging", "Needs test coverage"},
		SkillsNeeded: []string{"Go", "Testing"},
		TimeEstimate: "2-4 hours",
		ServerUsed:   "test-server",
		Confidence:   0.75,
	}

	if assessment.Level != "medium" {
		t.Errorf("Level = %v, want medium", assessment.Level)
	}

	if assessment.Score < 0 || assessment.Score > 1 {
		t.Errorf("Score should be between 0 and 1, got %v", assessment.Score)
	}
}

func TestAICommentGenerator_New(t *testing.T) {
	generator := NewAICommentGenerator(nil)

	if generator == nil {
		t.Error("NewAICommentGenerator should return non-nil generator")
	}

	if !generator.aiPreferred {
		t.Error("aiPreferred should be true by default")
	}
}

func TestAICommentGenerator_SetAIPreferred(t *testing.T) {
	generator := NewAICommentGenerator(nil)

	generator.SetAIPreferred(false)
	if generator.aiPreferred {
		t.Error("aiPreferred should be false")
	}

	generator.SetAIPreferred(true)
	if !generator.aiPreferred {
		t.Error("aiPreferred should be true")
	}
}

func TestAICommentGenerator_GenerateComment(t *testing.T) {
	generator := NewAICommentGenerator(nil)

	details := IssueDetails{
		Title:        "Bug in HandleRequest",
		Body:         "The HandleRequest function crashes",
		Labels:       []string{"bug"},
		Number:       123,
		ProjectOwner: "owner",
		ProjectName:  "repo",
	}

	result, err := generator.GenerateComment(details)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if result == nil {
		t.Error("GenerateComment should return non-nil result")
		return
	}

	if result.Body == "" {
		t.Error("Body should not be empty")
	}
}

func TestAICommentGenerator_GenerateWithAnalysis(t *testing.T) {
	generator := NewAICommentGenerator(nil)

	details := IssueDetails{
		Title:        "Bug in system",
		Body:         "Test body",
		Labels:       []string{"bug"},
		Number:       123,
		ProjectOwner: "owner",
		ProjectName:  "repo",
	}

	comment, summary, difficulty, err := generator.GenerateWithAnalysis(details)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if comment == nil {
		t.Error("comment should not be nil")
	}

	if summary == nil {
		t.Error("summary should not be nil")
	}

	if difficulty == nil {
		t.Error("difficulty should not be nil")
	}
}

func TestAIEnhancerStats(t *testing.T) {
	stats := AIEnhancerStats{
		TotalRequests:      100,
		SuccessfulRequests: 80,
		FallbackRequests:   15,
		FailedRequests:     5,
		TotalDuration:      5 * time.Second,
	}

	if stats.TotalRequests != 100 {
		t.Errorf("TotalRequests = %d, want 100", stats.TotalRequests)
	}

	if stats.SuccessfulRequests+stats.FallbackRequests+stats.FailedRequests != stats.TotalRequests {
		t.Error("request counts should add up to total")
	}
}
