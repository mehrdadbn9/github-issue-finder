package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type AIEnhancer struct {
	mcpClient       *MCPClient
	fallbackEnabled bool
	mu              sync.RWMutex
	stats           AIEnhancerStats
}

type AIEnhancerStats struct {
	TotalRequests      int64
	SuccessfulRequests int64
	FallbackRequests   int64
	FailedRequests     int64
	TotalDuration      time.Duration
}

type EnhancedComment struct {
	Body           string
	OriginalBody   string
	Enhancements   []string
	QualityScore   float64
	AIGenerated    bool
	ServerUsed     string
	GenerationTime time.Duration
}

type IssueSummary struct {
	Summary       string
	KeyPoints     []string
	SuggestedArea string
	Difficulty    string
	Confidence    float64
	ServerUsed    string
}

type SolutionSuggestion struct {
	Approach      string
	Steps         []string
	FilesToModify []string
	Risks         []string
	Confidence    float64
	ServerUsed    string
}

type DifficultyAssessment struct {
	Level        string
	Score        float64
	Reasons      []string
	SkillsNeeded []string
	TimeEstimate string
	ServerUsed   string
	Confidence   float64
}

func NewAIEnhancer(mcpClient *MCPClient) *AIEnhancer {
	return &AIEnhancer{
		mcpClient:       mcpClient,
		fallbackEnabled: true,
		stats:           AIEnhancerStats{},
	}
}

func (e *AIEnhancer) SetFallbackEnabled(enabled bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fallbackEnabled = enabled
}

func (e *AIEnhancer) EnhanceComment(details IssueDetails, draftComment string) *EnhancedComment {
	start := time.Now()

	e.mu.Lock()
	e.stats.TotalRequests++
	e.mu.Unlock()

	result := &EnhancedComment{
		OriginalBody: draftComment,
	}

	if e.mcpClient == nil || !e.mcpClient.IsAnyConnected() {
		e.mu.Lock()
		e.stats.FallbackRequests++
		e.mu.Unlock()
		return e.fallbackEnhanceComment(details, draftComment)
	}

	params := map[string]interface{}{
		"issue_title":   details.Title,
		"issue_body":    details.Body,
		"draft_comment": draftComment,
		"issue_labels":  details.Labels,
		"repository":    fmt.Sprintf("%s/%s", details.ProjectOwner, details.ProjectName),
		"issue_number":  details.Number,
	}

	callResult := e.mcpClient.CallToolOnAny("enhance_comment", params)

	if callResult.Error != nil {
		e.mu.Lock()
		if e.fallbackEnabled {
			e.stats.FallbackRequests++
		} else {
			e.stats.FailedRequests++
		}
		e.mu.Unlock()

		if e.fallbackEnabled {
			return e.fallbackEnhanceComment(details, draftComment)
		}

		result.Body = draftComment
		result.AIGenerated = false
		return result
	}

	e.mu.Lock()
	e.stats.SuccessfulRequests++
	e.stats.TotalDuration += time.Since(start)
	e.mu.Unlock()

	result.Body = callResult.Content
	result.AIGenerated = true
	result.ServerUsed = callResult.ServerName
	result.GenerationTime = callResult.Duration

	var enhancements []string
	if callResult.Content != draftComment {
		enhancements = append(enhancements, "AI-enhanced clarity and structure")
		if len(callResult.Content) > len(draftComment) {
			enhancements = append(enhancements, "Added more technical context")
		}
	}
	result.Enhancements = enhancements
	result.QualityScore = e.calculateQualityScore(callResult.Content, details)

	return result
}

func (e *AIEnhancer) SummarizeIssue(details IssueDetails) *IssueSummary {
	start := time.Now()

	e.mu.Lock()
	e.stats.TotalRequests++
	e.mu.Unlock()

	if e.mcpClient == nil || !e.mcpClient.IsAnyConnected() {
		e.mu.Lock()
		e.stats.FallbackRequests++
		e.mu.Unlock()
		return e.fallbackSummarizeIssue(details)
	}

	params := map[string]interface{}{
		"issue_title":  details.Title,
		"issue_body":   details.Body,
		"issue_labels": details.Labels,
		"repository":   fmt.Sprintf("%s/%s", details.ProjectOwner, details.ProjectName),
		"author":       details.Author,
		"comments":     details.Comments,
	}

	callResult := e.mcpClient.CallToolOnAny("summarize_issue", params)

	if callResult.Error != nil {
		e.mu.Lock()
		if e.fallbackEnabled {
			e.stats.FallbackRequests++
		} else {
			e.stats.FailedRequests++
		}
		e.mu.Unlock()

		if e.fallbackEnabled {
			return e.fallbackSummarizeIssue(details)
		}

		return &IssueSummary{
			Summary:    details.Title,
			Confidence: 0.0,
		}
	}

	e.mu.Lock()
	e.stats.SuccessfulRequests++
	e.stats.TotalDuration += time.Since(start)
	e.mu.Unlock()

	summary := e.parseSummaryResponse(callResult.Content, details)
	summary.ServerUsed = callResult.ServerName

	return summary
}

func (e *AIEnhancer) SuggestApproach(details IssueDetails) *SolutionSuggestion {
	start := time.Now()

	e.mu.Lock()
	e.stats.TotalRequests++
	e.mu.Unlock()

	if e.mcpClient == nil || !e.mcpClient.IsAnyConnected() {
		e.mu.Lock()
		e.stats.FallbackRequests++
		e.mu.Unlock()
		return e.fallbackSuggestApproach(details)
	}

	params := map[string]interface{}{
		"issue_title":  details.Title,
		"issue_body":   details.Body,
		"issue_labels": details.Labels,
		"repository":   fmt.Sprintf("%s/%s", details.ProjectOwner, details.ProjectName),
		"issue_url":    details.URL,
	}

	callResult := e.mcpClient.CallToolOnAny("suggest_approach", params)

	if callResult.Error != nil {
		e.mu.Lock()
		if e.fallbackEnabled {
			e.stats.FallbackRequests++
		} else {
			e.stats.FailedRequests++
		}
		e.mu.Unlock()

		if e.fallbackEnabled {
			return e.fallbackSuggestApproach(details)
		}

		return &SolutionSuggestion{
			Approach:   "Review the issue details and propose a solution",
			Confidence: 0.0,
		}
	}

	e.mu.Lock()
	e.stats.SuccessfulRequests++
	e.stats.TotalDuration += time.Since(start)
	e.mu.Unlock()

	suggestion := e.parseSuggestionResponse(callResult.Content, details)
	suggestion.ServerUsed = callResult.ServerName

	return suggestion
}

func (e *AIEnhancer) AnalyzeDifficulty(details IssueDetails) *DifficultyAssessment {
	start := time.Now()

	e.mu.Lock()
	e.stats.TotalRequests++
	e.mu.Unlock()

	if e.mcpClient == nil || !e.mcpClient.IsAnyConnected() {
		e.mu.Lock()
		e.stats.FallbackRequests++
		e.mu.Unlock()
		return e.fallbackAnalyzeDifficulty(details)
	}

	params := map[string]interface{}{
		"issue_title":  details.Title,
		"issue_body":   details.Body,
		"issue_labels": details.Labels,
		"repository":   fmt.Sprintf("%s/%s", details.ProjectOwner, details.ProjectName),
		"has_assignee": details.HasAssignee,
	}

	callResult := e.mcpClient.CallToolOnAny("analyze_difficulty", params)

	if callResult.Error != nil {
		e.mu.Lock()
		if e.fallbackEnabled {
			e.stats.FallbackRequests++
		} else {
			e.stats.FailedRequests++
		}
		e.mu.Unlock()

		if e.fallbackEnabled {
			return e.fallbackAnalyzeDifficulty(details)
		}

		return &DifficultyAssessment{
			Level:      "Unknown",
			Score:      0.5,
			Confidence: 0.0,
		}
	}

	e.mu.Lock()
	e.stats.SuccessfulRequests++
	e.stats.TotalDuration += time.Since(start)
	e.mu.Unlock()

	assessment := e.parseDifficultyResponse(callResult.Content, details)
	assessment.ServerUsed = callResult.ServerName

	return assessment
}

func (e *AIEnhancer) fallbackEnhanceComment(details IssueDetails, draftComment string) *EnhancedComment {
	generator := NewSmartCommentGenerator()
	smartComment, err := generator.GenerateSmartComment(details)
	if err != nil {
		return &EnhancedComment{
			Body:         draftComment,
			OriginalBody: draftComment,
			AIGenerated:  false,
		}
	}

	return &EnhancedComment{
		Body:         smartComment.Body,
		OriginalBody: draftComment,
		QualityScore: smartComment.Score,
		AIGenerated:  false,
		Enhancements: []string{"Used local smart comment generator"},
	}
}

func (e *AIEnhancer) fallbackSummarizeIssue(details IssueDetails) *IssueSummary {
	generator := NewSmartCommentGenerator()
	issueType := generator.classifyIssueType(details)

	var keyPoints []string
	title := details.Title
	if len(title) > 100 {
		title = title[:97] + "..."
	}
	keyPoints = append(keyPoints, fmt.Sprintf("Issue type: %s", issueType))

	if details.HasAssignee {
		keyPoints = append(keyPoints, "Already has an assignee")
	}
	if details.HasLinkedPR {
		keyPoints = append(keyPoints, "Has linked pull request")
	}
	if details.Comments > 0 {
		keyPoints = append(keyPoints, fmt.Sprintf("%d comments in discussion", details.Comments))
	}

	return &IssueSummary{
		Summary:       title,
		KeyPoints:     keyPoints,
		SuggestedArea: string(issueType),
		Difficulty:    "medium",
		Confidence:    0.5,
	}
}

func (e *AIEnhancer) fallbackSuggestApproach(details IssueDetails) *SolutionSuggestion {
	generator := NewSmartCommentGenerator()
	extracted := generator.extractTechnicalDetails(details)

	var steps []string
	var filesToModify []string

	if len(extracted.Files) > 0 {
		filesToModify = extracted.Files
		steps = append(steps, fmt.Sprintf("Review the affected files: %s", strings.Join(extracted.Files, ", ")))
	}

	if len(extracted.Functions) > 0 {
		steps = append(steps, fmt.Sprintf("Focus on the function: %s", extracted.Functions[0]))
	}

	if len(extracted.Errors) > 0 {
		steps = append(steps, "Investigate the error messages")
	}

	steps = append(steps, "Write comprehensive tests for the fix")
	steps = append(steps, "Submit a pull request for review")

	return &SolutionSuggestion{
		Approach:      "Follow standard debugging and fix workflow",
		Steps:         steps,
		FilesToModify: filesToModify,
		Risks:         []string{"May require coordination with maintainers"},
		Confidence:    0.5,
	}
}

func (e *AIEnhancer) fallbackAnalyzeDifficulty(details IssueDetails) *DifficultyAssessment {
	generator := NewSmartCommentGenerator()
	issueType := generator.classifyIssueType(details)

	level := "medium"
	score := 0.5
	var reasons []string
	var skillsNeeded []string

	switch issueType {
	case IssueTypeBug:
		level = "medium"
		score = 0.5
		reasons = append(reasons, "Bug fixes require debugging and investigation")
		skillsNeeded = append(skillsNeeded, "Debugging", "Code analysis")
	case IssueTypeFeature:
		level = "hard"
		score = 0.7
		reasons = append(reasons, "New features require design and implementation")
		skillsNeeded = append(skillsNeeded, "Software design", "Implementation")
	case IssueTypeDocs:
		level = "easy"
		score = 0.3
		reasons = append(reasons, "Documentation changes are typically straightforward")
		skillsNeeded = append(skillsNeeded, "Technical writing")
	case IssueTypePerformance:
		level = "hard"
		score = 0.75
		reasons = append(reasons, "Performance issues require profiling and optimization")
		skillsNeeded = append(skillsNeeded, "Performance profiling", "Optimization")
	case IssueTypeSecurity:
		level = "hard"
		score = 0.8
		reasons = append(reasons, "Security issues require careful handling")
		skillsNeeded = append(skillsNeeded, "Security analysis", "Secure coding")
	}

	if len(details.Labels) > 0 {
		for _, label := range details.Labels {
			labelLower := strings.ToLower(label)
			if strings.Contains(labelLower, "good first issue") || strings.Contains(labelLower, "beginner") {
				level = "easy"
				score = 0.25
				reasons = append(reasons, "Marked as good first issue")
			}
			if strings.Contains(labelLower, "help wanted") {
				reasons = append(reasons, "Help wanted - maintainers are available for guidance")
			}
		}
	}

	return &DifficultyAssessment{
		Level:        level,
		Score:        score,
		Reasons:      reasons,
		SkillsNeeded: skillsNeeded,
		TimeEstimate: e.estimateTime(level),
	}
}

func (e *AIEnhancer) calculateQualityScore(comment string, details IssueDetails) float64 {
	score := 0.5

	if strings.Contains(comment, "`") {
		score += 0.1
	}
	if strings.Contains(comment, "?") {
		score += 0.05
	}
	if len(comment) > 100 {
		score += 0.1
	}
	if len(comment) > 200 {
		score += 0.05
	}

	titleWords := strings.Fields(strings.ToLower(details.Title))
	commentLower := strings.ToLower(comment)
	for _, word := range titleWords {
		if len(word) > 4 && strings.Contains(commentLower, word) {
			score += 0.05
			break
		}
	}

	if score > 1.0 {
		score = 1.0
	}

	return score
}

func (e *AIEnhancer) parseSummaryResponse(content string, details IssueDetails) *IssueSummary {
	summary := &IssueSummary{
		Summary:    content,
		Confidence: 0.8,
	}

	var parsed struct {
		Summary    string   `json:"summary"`
		KeyPoints  []string `json:"key_points"`
		Area       string   `json:"suggested_area"`
		Difficulty string   `json:"difficulty"`
	}

	if err := json.Unmarshal([]byte(content), &parsed); err == nil {
		if parsed.Summary != "" {
			summary.Summary = parsed.Summary
		}
		summary.KeyPoints = parsed.KeyPoints
		summary.SuggestedArea = parsed.Area
		summary.Difficulty = parsed.Difficulty
	} else {
		summary.KeyPoints = e.extractKeyPointsFromText(content)
	}

	if summary.Summary == "" {
		summary.Summary = details.Title
	}

	return summary
}

func (e *AIEnhancer) parseSuggestionResponse(content string, details IssueDetails) *SolutionSuggestion {
	suggestion := &SolutionSuggestion{
		Approach:   content,
		Confidence: 0.8,
	}

	var parsed struct {
		Approach      string   `json:"approach"`
		Steps         []string `json:"steps"`
		FilesToModify []string `json:"files_to_modify"`
		Risks         []string `json:"risks"`
	}

	if err := json.Unmarshal([]byte(content), &parsed); err == nil {
		if parsed.Approach != "" {
			suggestion.Approach = parsed.Approach
		}
		suggestion.Steps = parsed.Steps
		suggestion.FilesToModify = parsed.FilesToModify
		suggestion.Risks = parsed.Risks
	} else {
		suggestion.Steps = e.extractStepsFromText(content)
	}

	if suggestion.Approach == "" {
		suggestion.Approach = "Review the issue and propose a solution"
	}

	return suggestion
}

func (e *AIEnhancer) parseDifficultyResponse(content string, details IssueDetails) *DifficultyAssessment {
	assessment := &DifficultyAssessment{
		Level:      "medium",
		Score:      0.5,
		Confidence: 0.8,
	}

	var parsed struct {
		Level        string   `json:"level"`
		Score        float64  `json:"score"`
		Reasons      []string `json:"reasons"`
		SkillsNeeded []string `json:"skills_needed"`
		TimeEstimate string   `json:"time_estimate"`
	}

	if err := json.Unmarshal([]byte(content), &parsed); err == nil {
		if parsed.Level != "" {
			assessment.Level = parsed.Level
		}
		if parsed.Score > 0 {
			assessment.Score = parsed.Score
		}
		assessment.Reasons = parsed.Reasons
		assessment.SkillsNeeded = parsed.SkillsNeeded
		assessment.TimeEstimate = parsed.TimeEstimate
	} else {
		contentLower := strings.ToLower(content)
		if strings.Contains(contentLower, "easy") || strings.Contains(contentLower, "simple") {
			assessment.Level = "easy"
			assessment.Score = 0.3
		} else if strings.Contains(contentLower, "hard") || strings.Contains(contentLower, "complex") {
			assessment.Level = "hard"
			assessment.Score = 0.7
		}
	}

	if assessment.TimeEstimate == "" {
		assessment.TimeEstimate = e.estimateTime(assessment.Level)
	}

	return assessment
}

func (e *AIEnhancer) extractKeyPointsFromText(text string) []string {
	var points []string
	lines := strings.Split(text, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
			point := strings.TrimPrefix(line, "- ")
			point = strings.TrimPrefix(point, "* ")
			point = strings.TrimSpace(point)
			if len(point) > 3 {
				points = append(points, point)
			}
		}
		for i := 1; i <= 20; i++ {
			prefix := fmt.Sprintf("%d. ", i)
			if strings.HasPrefix(line, prefix) {
				point := strings.TrimPrefix(line, prefix)
				point = strings.TrimSpace(point)
				if len(point) > 3 {
					points = append(points, point)
				}
				break
			}
		}
	}

	if len(points) == 0 && len(text) > 50 {
		words := strings.Fields(text)
		if len(words) > 20 {
			points = append(points, strings.Join(words[:20], " ")+"...")
		}
	}

	return points
}

func (e *AIEnhancer) extractStepsFromText(text string) []string {
	var steps []string
	lines := strings.Split(text, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
			step := strings.TrimPrefix(line, "- ")
			step = strings.TrimPrefix(step, "* ")
			if len(step) > 5 {
				steps = append(steps, step)
			}
		}

		for i := 1; i <= 10; i++ {
			prefix := fmt.Sprintf("%d. ", i)
			if strings.HasPrefix(line, prefix) {
				step := strings.TrimPrefix(line, prefix)
				if len(step) > 5 {
					steps = append(steps, step)
				}
				break
			}
		}
	}

	return steps
}

func (e *AIEnhancer) estimateTime(level string) string {
	switch level {
	case "easy":
		return "1-2 hours"
	case "medium":
		return "2-4 hours"
	case "hard":
		return "1-2 days"
	default:
		return "varies"
	}
}

func (e *AIEnhancer) GetStats() AIEnhancerStats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.stats
}

func (e *AIEnhancer) ResetStats() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stats = AIEnhancerStats{}
}

func (e *AIEnhancer) IsAvailable() bool {
	if e.mcpClient == nil {
		return false
	}
	return e.mcpClient.IsAnyConnected()
}

type AICommentGenerator struct {
	enhancer    *AIEnhancer
	generator   *SmartCommentGenerator
	aiPreferred bool
}

func NewAICommentGenerator(enhancer *AIEnhancer) *AICommentGenerator {
	return &AICommentGenerator{
		enhancer:    enhancer,
		generator:   NewSmartCommentGenerator(),
		aiPreferred: true,
	}
}

func (g *AICommentGenerator) SetAIPreferred(preferred bool) {
	g.aiPreferred = preferred
}

func (g *AICommentGenerator) GenerateComment(details IssueDetails) (*EnhancedComment, error) {
	baseComment, err := g.generator.GenerateSmartComment(details)
	if err != nil {
		return nil, err
	}

	if g.aiPreferred && g.enhancer != nil && g.enhancer.IsAvailable() {
		return g.enhancer.EnhanceComment(details, baseComment.Body), nil
	}

	return &EnhancedComment{
		Body:         baseComment.Body,
		OriginalBody: baseComment.Body,
		QualityScore: baseComment.Score,
		AIGenerated:  false,
		Enhancements: baseComment.QualityFlags,
	}, nil
}

func (g *AICommentGenerator) GenerateWithAnalysis(details IssueDetails) (*EnhancedComment, *IssueSummary, *DifficultyAssessment, error) {
	comment, err := g.GenerateComment(details)
	if err != nil {
		return nil, nil, nil, err
	}

	var summary *IssueSummary
	var difficulty *DifficultyAssessment

	if g.enhancer != nil && g.enhancer.IsAvailable() {
		summary = g.enhancer.SummarizeIssue(details)
		difficulty = g.enhancer.AnalyzeDifficulty(details)
	} else {
		summary = g.enhancer.fallbackSummarizeIssue(details)
		difficulty = g.enhancer.fallbackAnalyzeDifficulty(details)
	}

	return comment, summary, difficulty, nil
}
