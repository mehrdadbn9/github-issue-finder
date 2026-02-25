// Package main contains the smart comment generation system for GitHub issues.
// It analyzes issue details and generates context-aware, high-quality comments
// that are specific to the issue at hand and avoid generic phrasing.
package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// IssueType categorizes GitHub issues into different types for targeted comment generation.
type IssueType string

const (
	IssueTypeBug         IssueType = "bug"
	IssueTypeFeature     IssueType = "feature"
	IssueTypeDocs        IssueType = "documentation"
	IssueTypePerformance IssueType = "performance"
	IssueTypeSecurity    IssueType = "security"
	IssueTypeEnhancement IssueType = "enhancement"
	IssueTypeQuestion    IssueType = "question"
	IssueTypeUnknown     IssueType = "unknown"
)

// IssueDetails contains comprehensive information about a GitHub issue including
// title, body, labels, author, and metadata used for smart comment generation.
type IssueDetails struct {
	Title        string    // Issue title
	Body         string    // Issue description/body text
	Labels       []string  // Issue labels (bug, feature, etc.)
	Number       int       // Issue number in the repository
	URL          string    // Full GitHub URL to the issue
	ProjectOwner string    // GitHub organization/owner name
	ProjectName  string    // GitHub repository name
	Author       string    // Issue creator username
	CreatedAt    time.Time // When the issue was created
	Comments     int       // Number of existing comments on the issue
	HasAssignee  bool      // Whether the issue has an assignee
	HasLinkedPR  bool      // Whether the issue has a linked pull request
}

// SmartComment represents a generated comment with quality metrics and metadata.
// It includes the comment body, quality score, issue classification, and any warnings.
type SmartComment struct {
	Body         string    // The actual comment text to be posted
	Score        float64   // Quality score from 0.0 to 1.0
	IssueType    IssueType // Classified issue type
	QualityFlags []string  // Issues found during quality assessment
	Warnings     []string  // Suggestions for improvement
	GeneratedAt  time.Time // When the comment was generated
}

// CommentQualityResult contains detailed quality assessment of a generated comment.
// It tracks acceptability, scores, issues found, and whether the comment sounds AI-generated.
type CommentQualityResult struct {
	IsAcceptable   bool     // Whether comment meets quality threshold
	Score          float64  // Overall quality score
	Issues         []string // Quality issues found
	Suggestions    []string // Improvement suggestions
	IsAISounding   bool     // Detected as AI-generated text
	IsGeneric      bool     // Contains generic/non-specific phrases
	HasSpecificity bool     // References specific technical details from issue
}

// SmartCommentGenerator handles the generation of intelligent, context-aware comments
// for GitHub issues. It analyzes issue content, extracts technical details, classifies
// the issue type, and produces comments that are specific and avoid generic phrasing.
type SmartCommentGenerator struct {
	forbiddenPhrases   []string            // Phrases that reduce comment quality
	genericPhrases     []string            // Common generic phrases to avoid
	techPatterns       []*regexp.Regexp    // Regex patterns for technical term extraction
	commentHistory     map[string][]string // Track comments per issue to avoid repetition
	minQualityScore    float64             // Minimum acceptable quality score
	maxHistoryPerIssue int                 // Maximum comment history entries per issue
}

// NewSmartCommentGenerator creates and initializes a new SmartCommentGenerator with
// predefined patterns for detecting forbidden phrases, generic phrases, and technical terms.
// It sets up the comment history tracking and quality score thresholds.
func NewSmartCommentGenerator() *SmartCommentGenerator {
	return &SmartCommentGenerator{
		forbiddenPhrases: []string{
			"this issue is clear and actionable",
			"i'd like to work on this",
			"please assign me",
			"i can take this",
			"this looks interesting",
			"i'm interested in helping",
			"let me know if i can help",
			"happy to contribute",
			"great first issue",
			"looks good to me",
			"thanks for reporting",
			"thanks for opening this issue",
		},
		genericPhrases: []string{
			"i can investigate",
			"please assign if appropriate",
			"i can look into",
			"i'd be happy to help",
			"let me know if i can",
			"i can reproduce",
			"i can propose a solution",
			"according to rfc",
			"based on the documentation",
			"as per the specification",
		},
		techPatterns: []*regexp.Regexp{
			regexp.MustCompile(`func\s+(\w+)`),
			regexp.MustCompile(`method\s+(\w+)`),
			regexp.MustCompile(`struct\s+(\w+)`),
			regexp.MustCompile(`interface\s+(\w+)`),
			regexp.MustCompile(`package\s+(\w+)`),
			regexp.MustCompile(`file[:\s]+([^\s,]+)`),
			regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*\.go)`),
			regexp.MustCompile(`line[:\s]+(\d+)`),
			regexp.MustCompile(`error[:\s]+([^\n]+)`),
			regexp.MustCompile(`panic[:\s]+([^\n]+)`),
			regexp.MustCompile(`nil pointer`),
			regexp.MustCompile(`segmentation fault`),
			regexp.MustCompile(`memory leak`),
			regexp.MustCompile(`race condition`),
			regexp.MustCompile(`deadlock`),
		},
		commentHistory:     make(map[string][]string),
		minQualityScore:    0.6,
		maxHistoryPerIssue: 5,
	}
}

// ValidateIssueDetails ensures the provided issue details are valid and complete.
// Returns nil if valid, or an error describing what's missing or invalid.
func ValidateIssueDetails(details IssueDetails) error {
	if strings.TrimSpace(details.Title) == "" {
		return fmt.Errorf("issue title is required and cannot be empty")
	}

	if strings.TrimSpace(details.URL) == "" {
		return fmt.Errorf("issue URL is required and cannot be empty")
	}

	if details.Number <= 0 {
		return fmt.Errorf("issue number must be positive, got %d", details.Number)
	}

	if strings.TrimSpace(details.ProjectOwner) == "" {
		return fmt.Errorf("project owner is required and cannot be empty")
	}

	if strings.TrimSpace(details.ProjectName) == "" {
		return fmt.Errorf("project name is required and cannot be empty")
	}

	return nil
}

// CheckIfIssueHasSolution detects if an issue already has a working solution posted.
// It searches the issue body and comments for solution indicators such as:
// - References to fixed versions or PRs
// - "won't fix" or "duplicate" resolutions
// - "already implemented" or "resolved" statements
// - External solutions or workarounds that answer the original problem
// - ANY PR mentioned (even without "fixes" keyword) - indicates active work
// - Comments indicating active work on a solution
// Returns true if a real solution is detected, false otherwise.
func (g *SmartCommentGenerator) CheckIfIssueHasSolution(details IssueDetails) bool {
	// Early return if issue has a linked PR - indicates active work
	if details.HasLinkedPR {
		return true
	}

	// Combine issue body and title for comprehensive search
	fullText := strings.ToLower(details.Title + " " + details.Body)

	// Solution indicators that mark an issue as already having a working solution
	solutionPatterns := []string{
		// Resolved/Fixed patterns
		"resolved in",
		"fixed in",
		"fixed by",
		"this is fixed by",
		"this has been fixed",
		"this was fixed",
		"solution is to",
		"workaround is to",

		// Implementation patterns
		"already implemented",
		"already exists",
		"flux operator",
		"implemented in",

		// Resolution patterns
		"won't fix",
		"will not fix",
		"not a bug",
		"working as intended",
		"expected behavior",
		"by design",
		"duplicate of",
		"closing as",
		"resolved",
		"fixed",

		// PR/commit references
		"merged in",
		"merge request",
		"pull request",

		// Status patterns
		"closed:",
		"status: resolved",
		"status: fixed",
		"status: won't fix",
		"status: duplicate",
	}

	// Check if any solution pattern is present
	for _, pattern := range solutionPatterns {
		if strings.Contains(fullText, pattern) {
			return true
		}
	}

	// Enhanced PR detection - ANY PR mention means work is being done
	prNumberPattern := regexp.MustCompile(`#(\d{4,6})`)
	if prNumbers := prNumberPattern.FindAllStringSubmatch(details.Body, -1); len(prNumbers) > 0 {
		return true // Someone is already working on a PR
	}

	// GitHub PR URL pattern
	prURLPattern := regexp.MustCompile(`github\.com/[^/]+/[^/]+/pull/\d+`)
	if prURLPattern.MatchString(details.Body) {
		return true // Has linked PR
	}

	// Check for active work indicators
	workPatterns := []string{
		"someone already has a pr",
		"already working on",
		"pr active for",
		"i'm working on",
		"working on a fix",
		"i have a pr",
		"draft pr",
		"ready for review",
		"in review",
	}

	for _, pattern := range workPatterns {
		if strings.Contains(fullText, pattern) {
			return true
		}
	}

	// Check for PR links in issue body (indicates potential fix)
	prLinkPattern := regexp.MustCompile(`(#\d+|/pull/\d+|pulls/\d+)`)
	if prLinkPattern.MatchString(details.Body) {
		// Additional check: look for keywords that indicate this PR is fixing the issue
		if strings.Contains(fullText, "fixes") || strings.Contains(fullText, "closes") ||
			strings.Contains(fullText, "resolves") || strings.Contains(fullText, "fix") {
			return true
		}
	}

	// Check for code block solutions in comments (length and technical indicators)
	codeBlockPattern := regexp.MustCompile("```[^`]*```")
	codeBlocks := codeBlockPattern.FindAllString(details.Body, -1)
	for _, block := range codeBlocks {
		// If there's a code block with explanation of how to use it, likely a solution
		if len(block) > 200 && (strings.Contains(fullText, "use") || strings.Contains(fullText, "example") ||
			strings.Contains(fullText, "how to") || strings.Contains(fullText, "install")) {
			return true
		}
	}

	return false
}

// GenerateSmartComment creates an intelligent, context-aware comment for a GitHub issue.
// It first validates the issue details, checks if a solution already exists, extracts
// technical details, classifies the issue type, and generates a specific comment.
// If the generated comment doesn't meet quality standards, it attempts to improve it.
// Returns nil and an error if validation fails or a solution is already present.
func (g *SmartCommentGenerator) GenerateSmartComment(details IssueDetails) (*SmartComment, error) {
	// Validate issue details first
	if err := ValidateIssueDetails(details); err != nil {
		return nil, fmt.Errorf("invalid issue details: %w", err)
	}

	// Check if issue already has a solution - skip if it does
	if g.CheckIfIssueHasSolution(details) {
		return nil, fmt.Errorf("issue already has a working solution")
	}

	issueType := g.classifyIssueType(details)

	extractedDetails := g.extractTechnicalDetails(details)

	comment := g.generateTypeSpecificComment(details, issueType, extractedDetails)

	quality := g.ScoreComment(comment, details, extractedDetails)

	if quality.Score < g.minQualityScore {
		comment = g.improveComment(comment, details, issueType, extractedDetails, quality)
		quality = g.ScoreComment(comment, details, extractedDetails)
	}

	g.recordComment(details.URL, comment)

	return &SmartComment{
		Body:         comment,
		Score:        quality.Score,
		IssueType:    issueType,
		QualityFlags: quality.Issues,
		Warnings:     quality.Suggestions,
		GeneratedAt:  time.Now(),
	}, nil
}

// classifyIssueType analyzes the issue details and determines its type (bug, feature, etc.)
// by examining labels, title, and body content for type-specific keywords and patterns.
// Returns the most specific type match, defaulting to IssueTypeUnknown if no clear match.
func (g *SmartCommentGenerator) classifyIssueType(details IssueDetails) IssueType {
	titleLower := strings.ToLower(details.Title)
	bodyLower := strings.ToLower(details.Body)
	labelsLower := make([]string, len(details.Labels))
	for i, l := range details.Labels {
		labelsLower[i] = strings.ToLower(l)
	}

	for _, label := range labelsLower {
		if strings.Contains(label, "security") || strings.Contains(label, "cve") || strings.Contains(label, "vulnerability") {
			return IssueTypeSecurity
		}
		if strings.Contains(label, "performance") || strings.Contains(label, "perf") {
			return IssueTypePerformance
		}
		if strings.Contains(label, "documentation") || strings.Contains(label, "docs") {
			return IssueTypeDocs
		}
		if strings.Contains(label, "feature") || strings.Contains(label, "enhancement") {
			return IssueTypeFeature
		}
		if strings.Contains(label, "bug") {
			return IssueTypeBug
		}
	}

	if strings.Contains(titleLower, "security") || strings.Contains(bodyLower, "vulnerability") ||
		strings.Contains(bodyLower, "cve-") || strings.Contains(titleLower, "cve") {
		return IssueTypeSecurity
	}

	if strings.Contains(titleLower, "performance") || strings.Contains(titleLower, "slow") ||
		strings.Contains(titleLower, "latency") || strings.Contains(titleLower, "memory") ||
		strings.Contains(bodyLower, "benchmark") || strings.Contains(bodyLower, "throughput") {
		return IssueTypePerformance
	}

	if strings.Contains(titleLower, "doc") || strings.Contains(titleLower, "documentation") ||
		strings.Contains(titleLower, "readme") || strings.Contains(titleLower, "comment") ||
		strings.Contains(bodyLower, "documentation") {
		return IssueTypeDocs
	}

	if strings.Contains(titleLower, "feature") || strings.Contains(titleLower, "add support") ||
		strings.Contains(titleLower, "implement") || strings.Contains(titleLower, "support for") {
		return IssueTypeFeature
	}

	if strings.Contains(titleLower, "bug") || strings.Contains(titleLower, "fix") ||
		strings.Contains(titleLower, "crash") || strings.Contains(titleLower, "error") ||
		strings.Contains(titleLower, "panic") || strings.Contains(titleLower, "fail") ||
		strings.Contains(bodyLower, "steps to reproduce") || strings.Contains(bodyLower, "reproduce") {
		return IssueTypeBug
	}

	if strings.Contains(titleLower, "enhance") || strings.Contains(titleLower, "improve") {
		return IssueTypeEnhancement
	}

	return IssueTypeUnknown
}

// ExtractedDetails contains technical information extracted from an issue.
// It includes code references, errors, file names, and structural indicators
// that help make comments specific and relevant to the issue.
type ExtractedDetails struct {
	Functions     []string // Function/method names mentioned
	Files         []string // File paths referenced
	Methods       []string // Method names mentioned
	Structs       []string // Struct/class names mentioned
	Interfaces    []string // Interface names mentioned
	Packages      []string // Package/module names mentioned
	LineNumbers   []string // Specific line numbers referenced
	Errors        []string // Error messages mentioned
	Panics        []string // Panic/exception messages
	CodeBlocks    []string // Code snippets included
	HasReproSteps bool     // Issue includes reproduction steps
	HasStacktrace bool     // Issue includes stack trace or goroutine dump
	KeyPhrases    []string // Important phrases extracted from the issue
}

// extractTechnicalDetails analyzes the issue title and body to extract technical
// information such as function names, file paths, error messages, and code patterns.
// This helps generate specific, targeted comments rather than generic ones.
func (g *SmartCommentGenerator) extractTechnicalDetails(details IssueDetails) ExtractedDetails {
	extracted := ExtractedDetails{}
	text := details.Title + " " + details.Body

	funcMatches := g.techPatterns[0].FindAllStringSubmatch(text, -1)
	for _, m := range funcMatches {
		if len(m) > 1 {
			extracted.Functions = append(extracted.Functions, m[1])
		}
	}

	methodMatches := g.techPatterns[1].FindAllStringSubmatch(text, -1)
	for _, m := range methodMatches {
		if len(m) > 1 {
			extracted.Methods = append(extracted.Methods, m[1])
		}
	}

	structMatches := g.techPatterns[2].FindAllStringSubmatch(text, -1)
	for _, m := range structMatches {
		if len(m) > 1 {
			extracted.Structs = append(extracted.Structs, m[1])
		}
	}

	interfaceMatches := g.techPatterns[3].FindAllStringSubmatch(text, -1)
	for _, m := range interfaceMatches {
		if len(m) > 1 {
			extracted.Interfaces = append(extracted.Interfaces, m[1])
		}
	}

	packageMatches := g.techPatterns[4].FindAllStringSubmatch(text, -1)
	for _, m := range packageMatches {
		if len(m) > 1 {
			extracted.Packages = append(extracted.Packages, m[1])
		}
	}

	fileMatches := g.techPatterns[5].FindAllStringSubmatch(text, -1)
	for _, m := range fileMatches {
		if len(m) > 1 {
			extracted.Files = append(extracted.Files, m[1])
		}
	}

	goFileMatches := g.techPatterns[6].FindAllStringSubmatch(text, -1)
	for _, m := range goFileMatches {
		if len(m) > 1 {
			extracted.Files = append(extracted.Files, m[1])
		}
	}

	lineMatches := g.techPatterns[7].FindAllStringSubmatch(text, -1)
	for _, m := range lineMatches {
		if len(m) > 1 {
			extracted.LineNumbers = append(extracted.LineNumbers, m[1])
		}
	}

	errorMatches := g.techPatterns[8].FindAllStringSubmatch(text, -1)
	for _, m := range errorMatches {
		if len(m) > 1 {
			extracted.Errors = append(extracted.Errors, strings.TrimSpace(m[1]))
		}
	}

	panicMatches := g.techPatterns[9].FindAllStringSubmatch(text, -1)
	for _, m := range panicMatches {
		if len(m) > 1 {
			extracted.Panics = append(extracted.Panics, strings.TrimSpace(m[1]))
		}
	}

	codeBlockRegex := regexp.MustCompile("```[^`]*```")
	codeBlocks := codeBlockRegex.FindAllString(details.Body, -1)
	extracted.CodeBlocks = codeBlocks

	bodyLower := strings.ToLower(details.Body)
	extracted.HasReproSteps = strings.Contains(bodyLower, "steps to reproduce") ||
		strings.Contains(bodyLower, "reproduce") ||
		strings.Contains(bodyLower, "how to reproduce") ||
		strings.Contains(bodyLower, "reproduction")

	extracted.HasStacktrace = strings.Contains(bodyLower, "stack trace") ||
		strings.Contains(bodyLower, "stacktrace") ||
		strings.Contains(bodyLower, "goroutine") ||
		strings.Contains(bodyLower, "traceback")

	keyPhrases := g.extractKeyPhrases(details)
	extracted.KeyPhrases = keyPhrases

	return extracted
}

// extractKeyPhrases finds important phrases and information patterns from the issue
// including conditions, expected behavior, actual behavior, version info, and OS details.
// These phrases help identify the core problem and suggest relevant discussion points.
func (g *SmartCommentGenerator) extractKeyPhrases(details IssueDetails) []string {
	var phrases []string
	text := details.Title + " " + details.Body

	phrasePatterns := []struct {
		pattern *regexp.Regexp
		name    string
	}{
		{regexp.MustCompile(`(?i)when\s+(.+?)(?:,|\n|\.)`), "condition"},
		{regexp.MustCompile(`(?i)expected[:\s]+(.+?)(?:\n|\.)`), "expected"},
		{regexp.MustCompile(`(?i)actual[:\s]+(.+?)(?:\n|\.)`), "actual"},
		{regexp.MustCompile(`(?i)version[:\s]+([^\n]+)`), "version"},
		{regexp.MustCompile(`(?i)os[:\s]+([^\n]+)`), "os"},
		{regexp.MustCompile(`(?i)go\s+version[:\s]+([^\n]+)`), "go_version"},
	}

	for _, pp := range phrasePatterns {
		matches := pp.pattern.FindAllStringSubmatch(text, 3)
		for _, m := range matches {
			if len(m) > 1 && len(m[1]) > 2 && len(m[1]) < 100 {
				phrases = append(phrases, strings.TrimSpace(m[1]))
			}
		}
	}

	return phrases
}

// generateTypeSpecificComment creates a comment tailored to the specific issue type.
// Different issue types (bugs, features, docs, etc.) receive different comment templates
// and focused discussion points relevant to their category.
func (g *SmartCommentGenerator) generateTypeSpecificComment(details IssueDetails, issueType IssueType, extracted ExtractedDetails) string {
	switch issueType {
	case IssueTypeBug:
		return g.generateBugComment(details, extracted)
	case IssueTypeFeature:
		return g.generateFeatureComment(details, extracted)
	case IssueTypeDocs:
		return g.generateDocsComment(details, extracted)
	case IssueTypePerformance:
		return g.generatePerformanceComment(details, extracted)
	case IssueTypeSecurity:
		return g.generateSecurityComment(details, extracted)
	case IssueTypeEnhancement:
		return g.generateEnhancementComment(details, extracted)
	default:
		return g.generateGenericComment(details, extracted)
	}
}

// generateBugComment creates a targeted response for bug reports.
// It references specific functions, errors, or files when available, asks for
// additional context if needed, and offers to investigate the root cause.
func (g *SmartCommentGenerator) generateBugComment(details IssueDetails, extracted ExtractedDetails) string {
	var parts []string

	if len(extracted.Functions) > 0 {
		parts = append(parts, fmt.Sprintf("Looking at the issue in `%s()`", extracted.Functions[0]))
	} else if len(extracted.Files) > 0 {
		parts = append(parts, fmt.Sprintf("Examining the problem in %s", extracted.Files[0]))
	} else if len(extracted.Methods) > 0 {
		parts = append(parts, fmt.Sprintf("The issue appears to be in the `%s` method", extracted.Methods[0]))
	} else {
		titleWords := g.extractKeyNounPhrases(details.Title)
		if len(titleWords) > 0 {
			parts = append(parts, fmt.Sprintf("Regarding the %s issue", titleWords[0]))
		} else {
			parts = append(parts, "I've analyzed this bug report")
		}
	}

	if len(extracted.Errors) > 0 {
		parts = append(parts, fmt.Sprintf(" - the error `%s` indicates a problem", g.truncateError(extracted.Errors[0])))
	}

	if extracted.HasReproSteps {
		parts = append(parts, ". The reproduction steps are clear")
	} else {
		parts = append(parts, ". Could you share the exact steps to reproduce?")
	}

	if len(extracted.CodeBlocks) > 0 {
		parts = append(parts, ". I see you've included code samples which helps narrow down the issue")
	}

	if details.Comments == 0 {
		parts = append(parts, ". I'd like to investigate this further - is there any additional context about when this started occurring?")
	} else {
		parts = append(parts, ". Let me review the discussion and propose a fix approach.")
	}

	return strings.Join(parts, "") + "\n\nI have experience debugging similar issues in Go projects and can help trace the root cause."
}

// generateFeatureComment creates a response for feature requests.
// It asks clarifying questions about implementation details, API design, and
// compatibility requirements to help shape the feature development.
func (g *SmartCommentGenerator) generateFeatureComment(details IssueDetails, extracted ExtractedDetails) string {
	var parts []string

	titleLower := strings.ToLower(details.Title)

	if len(extracted.Packages) > 0 {
		parts = append(parts, fmt.Sprintf("For the proposed feature in the `%s` package", extracted.Packages[0]))
	} else if len(extracted.Interfaces) > 0 {
		parts = append(parts, fmt.Sprintf("Regarding the enhancement to `%s` interface", extracted.Interfaces[0]))
	} else if len(extracted.Structs) > 0 {
		parts = append(parts, fmt.Sprintf("For the feature request involving `%s`", extracted.Structs[0]))
	} else {
		featureDesc := g.extractFeatureDescription(details.Title)
		parts = append(parts, fmt.Sprintf("Regarding the %s feature request", featureDesc))
	}

	if strings.Contains(titleLower, "support") || strings.Contains(titleLower, "add") {
		parts = append(parts, ", I understand the need for this capability")
	} else {
		parts = append(parts, ", this would be a valuable addition")
	}

	parts = append(parts, ". A few questions to help shape the implementation:\n")

	questions := g.generateFeatureQuestions(details, extracted)
	for _, q := range questions {
		parts = append(parts, fmt.Sprintf("\n- %s", q))
	}

	parts = append(parts, "\n\nI can work on a draft implementation to explore the approach.")

	return strings.Join(parts, "")
}

// generateDocsComment creates a response for documentation issues.
// It identifies what type of documentation needs improvement and offers to
// draft changes with clear examples and explanations.
func (g *SmartCommentGenerator) generateDocsComment(details IssueDetails, extracted ExtractedDetails) string {
	var parts []string

	if len(extracted.Files) > 0 {
		parts = append(parts, fmt.Sprintf("I can help improve the documentation in %s", extracted.Files[0]))
	} else {
		titleLower := strings.ToLower(details.Title)
		if strings.Contains(titleLower, "readme") {
			parts = append(parts, "I can help update the README documentation")
		} else if strings.Contains(titleLower, "api") {
			parts = append(parts, "I can assist with the API documentation improvements")
		} else if strings.Contains(titleLower, "example") {
			parts = append(parts, "I can add documentation examples for this use case")
		} else {
			parts = append(parts, "I can help improve the documentation")
		}
	}

	bodyLower := strings.ToLower(details.Body)
	if strings.Contains(bodyLower, "missing") {
		parts = append(parts, " - the missing sections need to be added")
	} else if strings.Contains(bodyLower, "outdated") || strings.Contains(bodyLower, "incorrect") {
		parts = append(parts, " - the outdated information should be updated")
	} else if strings.Contains(bodyLower, "unclear") || strings.Contains(bodyLower, "confusing") {
		parts = append(parts, " - the unclear sections need clarification")
	}

	if len(extracted.Functions) > 0 || len(extracted.Methods) > 0 {
		target := ""
		if len(extracted.Functions) > 0 {
			target = extracted.Functions[0]
		} else {
			target = extracted.Methods[0]
		}
		parts = append(parts, fmt.Sprintf(". I'll focus on documenting `%s` with clear examples", target))
	}

	parts = append(parts, ".\n\nWould you like me to draft the documentation changes?")

	return strings.Join(parts, "")
}

// generatePerformanceComment creates a response for performance-related issues.
// It focuses on performance profiling, benchmarking, and optimization strategies
// appropriate to the specific performance concern (memory, latency, CPU, etc.).
func (g *SmartCommentGenerator) generatePerformanceComment(details IssueDetails, extracted ExtractedDetails) string {
	var parts []string

	bodyLower := strings.ToLower(details.Body)

	if len(extracted.Functions) > 0 {
		parts = append(parts, fmt.Sprintf("Looking at the performance issue in `%s()`", extracted.Functions[0]))
	} else if len(extracted.Files) > 0 {
		parts = append(parts, fmt.Sprintf("Analyzing the performance concern in %s", extracted.Files[0]))
	} else {
		parts = append(parts, "I've reviewed the performance issue")
	}

	if strings.Contains(bodyLower, "memory") || strings.Contains(bodyLower, "alloc") {
		parts = append(parts, " - memory allocation patterns are likely a factor")
	} else if strings.Contains(bodyLower, "latency") || strings.Contains(bodyLower, "slow") {
		parts = append(parts, " - latency optimization will be the focus")
	} else if strings.Contains(bodyLower, "cpu") || strings.Contains(bodyLower, "throughput") {
		parts = append(parts, " - CPU utilization needs investigation")
	}

	if strings.Contains(bodyLower, "benchmark") {
		parts = append(parts, ". The benchmarks provided help establish a baseline")
	} else {
		parts = append(parts, ". Could you share any benchmark results showing the current performance?")
	}

	if len(extracted.CodeBlocks) > 0 {
		parts = append(parts, "\n\nFrom the code sample, I can identify potential optimization opportunities")
	}

	parts = append(parts, ". I have experience with Go performance profiling using pprof and can help investigate.")

	return strings.Join(parts, "")
}

// generateSecurityComment creates a response for security vulnerabilities.
// It identifies the specific security concern (CVE, XSS, injection, auth, etc.)
// and commits to following secure coding practices and coordination procedures.
func (g *SmartCommentGenerator) generateSecurityComment(details IssueDetails, extracted ExtractedDetails) string {
	var parts []string

	titleLower := strings.ToLower(details.Title)
	bodyLower := strings.ToLower(details.Body)

	if strings.Contains(bodyLower, "cve-") || strings.Contains(titleLower, "cve") {
		cveMatch := regexp.MustCompile(`CVE-\d{4}-\d+`).FindString(bodyLower + titleLower)
		if cveMatch != "" {
			parts = append(parts, fmt.Sprintf("Regarding %s, I understand the security implications", cveMatch))
		} else {
			parts = append(parts, "I understand this CVE requires attention")
		}
	} else if strings.Contains(bodyLower, "xss") || strings.Contains(titleLower, "xss") {
		parts = append(parts, "I can help address this XSS vulnerability")
	} else if strings.Contains(bodyLower, "injection") || strings.Contains(titleLower, "injection") {
		parts = append(parts, "I can assist with fixing this injection vulnerability")
	} else if strings.Contains(bodyLower, "auth") || strings.Contains(titleLower, "authentication") {
		parts = append(parts, "I can help strengthen the authentication security")
	} else {
		parts = append(parts, "I understand the security concern and can help address it")
	}

	if len(extracted.Files) > 0 {
		parts = append(parts, fmt.Sprintf(" in %s", extracted.Files[0]))
	}

	parts = append(parts, ". Security issues require careful handling - I'll ensure the fix doesn't introduce new vulnerabilities.\n\n")
	parts = append(parts, "I have experience with secure coding practices in Go and can propose a fix that follows security best practices. Should I coordinate with the security team before publishing changes?")

	return strings.Join(parts, "")
}

// generateEnhancementComment creates a response for enhancement requests.
// It proposes a specific implementation approach and offers to start with
// a proof-of-concept implementation.
func (g *SmartCommentGenerator) generateEnhancementComment(details IssueDetails, extracted ExtractedDetails) string {
	var parts []string

	if len(extracted.Functions) > 0 {
		parts = append(parts, fmt.Sprintf("For the enhancement to `%s()`", extracted.Functions[0]))
	} else if len(extracted.Structs) > 0 {
		parts = append(parts, fmt.Sprintf("Regarding the improvement to `%s`", extracted.Structs[0]))
	} else {
		parts = append(parts, "I've reviewed this enhancement proposal")
	}

	parts = append(parts, ", the scope is well-defined")

	if len(extracted.KeyPhrases) > 0 {
		parts = append(parts, fmt.Sprintf(". The requirement for %s is clear", g.truncatePhrase(extracted.KeyPhrases[0])))
	}

	parts = append(parts, ". I have some thoughts on the implementation approach:\n")

	approach := g.suggestApproach(details, extracted)
	parts = append(parts, fmt.Sprintf("\n**Proposed approach:** %s", approach))

	parts = append(parts, "\n\nWould you like me to start with a proof-of-concept implementation?")

	return strings.Join(parts, "")
}

// generateGenericComment creates a fallback response for issues that don't fit
// other categories. It still references specific technical details when available
// and offers to help with unspecified ways.
func (g *SmartCommentGenerator) generateGenericComment(details IssueDetails, extracted ExtractedDetails) string {
	var parts []string

	if len(extracted.Functions) > 0 {
		parts = append(parts, fmt.Sprintf("Looking at `%s()`", extracted.Functions[0]))
	} else if len(extracted.Files) > 0 {
		parts = append(parts, fmt.Sprintf("Regarding %s", extracted.Files[0]))
	} else if len(extracted.Packages) > 0 {
		parts = append(parts, fmt.Sprintf("In the `%s` package", extracted.Packages[0]))
	} else {
		titleWords := g.extractKeyNounPhrases(details.Title)
		if len(titleWords) > 0 {
			parts = append(parts, fmt.Sprintf("Regarding the %s", titleWords[0]))
		} else {
			parts = append(parts, "I've reviewed this issue")
		}
	}

	if details.Comments > 0 {
		parts = append(parts, fmt.Sprintf(" - the discussion has %d comments already. I've reviewed the context", details.Comments))
	}

	if len(extracted.Errors) > 0 {
		parts = append(parts, fmt.Sprintf(" and noticed the error: `%s`", g.truncateError(extracted.Errors[0])))
	}

	parts = append(parts, ". I can help with this - any specific requirements or constraints I should be aware of?")

	return strings.Join(parts, "")
}

// ScoreComment evaluates the quality of a generated comment based on multiple criteria:
// - Avoidance of forbidden and generic phrases
// - Inclusion of specific technical details
// - Appropriate length and formatting
// - Non-repetition from comment history
// Returns a CommentQualityResult with detailed quality metrics and suggestions.
func (g *SmartCommentGenerator) ScoreComment(comment string, details IssueDetails, extracted ExtractedDetails) CommentQualityResult {
	result := CommentQualityResult{
		Score:       1.0,
		Issues:      []string{},
		Suggestions: []string{},
	}

	commentLower := strings.ToLower(comment)

	for _, forbidden := range g.forbiddenPhrases {
		if strings.Contains(commentLower, strings.ToLower(forbidden)) {
			result.Score -= 0.5
			result.Issues = append(result.Issues, fmt.Sprintf("Contains forbidden phrase: '%s'", forbidden))
			result.IsGeneric = true
		}
	}

	genericCount := 0
	for _, generic := range g.genericPhrases {
		if strings.Contains(commentLower, strings.ToLower(generic)) {
			genericCount++
		}
	}
	if genericCount >= 2 {
		result.Score -= 0.3
		result.IsAISounding = true
		result.Issues = append(result.Issues, "Uses multiple generic phrases - sounds AI-generated")
	} else if genericCount == 1 {
		result.Score -= 0.1
	}

	hasSpecificity := g.checkSpecificity(comment, details, extracted)
	if !hasSpecificity {
		result.Score -= 0.2
		result.HasSpecificity = false
		result.Issues = append(result.Issues, "Comment lacks issue-specific technical details")
	} else {
		result.HasSpecificity = true
		result.Score += 0.1
	}

	if strings.Contains(comment, "`") || strings.Contains(comment, "**") {
		result.Score += 0.05
	}

	if strings.Contains(comment, "?") {
		result.Score += 0.05
	}

	if len(comment) < 50 {
		result.Score -= 0.3
		result.Issues = append(result.Issues, "Comment is too short - lacks substance")
	} else if len(comment) > 500 {
		result.Score -= 0.1
		result.Issues = append(result.Issues, "Comment may be too verbose")
	}

	if g.isRepetitive(comment, details.URL) {
		result.Score -= 0.4
		result.Issues = append(result.Issues, "Comment appears repetitive based on history")
	}

	if result.Score < 0 {
		result.Score = 0
	}
	if result.Score > 1 {
		result.Score = 1
	}

	result.IsAcceptable = result.Score >= g.minQualityScore

	if !result.IsAcceptable {
		result.Suggestions = g.generateQualitySuggestions(result, details, extracted)
	}

	return result
}

// checkSpecificity determines if a comment references specific technical details
// from the issue (functions, files, errors) rather than generic concepts.
// Returns true if comment contains issue-specific technical references.
func (g *SmartCommentGenerator) checkSpecificity(comment string, details IssueDetails, extracted ExtractedDetails) bool {
	commentLower := strings.ToLower(comment)
	detailsLower := strings.ToLower(details.Title + " " + details.Body)

	if len(extracted.Functions) > 0 {
		for _, fn := range extracted.Functions {
			if strings.Contains(commentLower, strings.ToLower(fn)) {
				return true
			}
		}
	}

	if len(extracted.Files) > 0 {
		for _, f := range extracted.Files {
			if strings.Contains(commentLower, strings.ToLower(f)) {
				return true
			}
		}
	}

	if len(extracted.Errors) > 0 {
		for _, e := range extracted.Errors {
			if strings.Contains(commentLower, strings.ToLower(e)) {
				return true
			}
		}
	}

	keyWords := g.extractKeyNounPhrases(details.Title)
	for _, kw := range keyWords {
		if len(kw) > 3 && strings.Contains(commentLower, strings.ToLower(kw)) {
			return true
		}
	}

	if strings.Contains(detailsLower, "performance") && strings.Contains(commentLower, "performance") {
		return true
	}
	if strings.Contains(detailsLower, "security") && strings.Contains(commentLower, "security") {
		return true
	}
	if strings.Contains(detailsLower, "documentation") && strings.Contains(commentLower, "documentation") {
		return true
	}

	return false
}

// isRepetitive checks if a comment is too similar to previously posted comments
// on the same issue. Uses exact match and similarity calculation to detect repetition.
// Returns true if the comment is overly similar to historical comments.
func (g *SmartCommentGenerator) isRepetitive(comment string, issueURL string) bool {
	history, exists := g.commentHistory[issueURL]
	if !exists {
		return false
	}

	commentLower := strings.ToLower(strings.TrimSpace(comment))
	for _, past := range history {
		pastLower := strings.ToLower(strings.TrimSpace(past))
		if commentLower == pastLower {
			return true
		}

		similarity := g.calculateSimilarity(commentLower, pastLower)
		if similarity > 0.8 {
			return true
		}
	}

	return false
}

// calculateSimilarity computes the Jaccard-like similarity between two text strings
// based on word overlap. Returns a value between 0 and 1, where 1 means identical.
func (g *SmartCommentGenerator) calculateSimilarity(s1, s2 string) float64 {
	words1 := strings.Fields(s1)
	words2 := strings.Fields(s2)

	if len(words1) == 0 || len(words2) == 0 {
		return 0
	}

	set1 := make(map[string]bool)
	for _, w := range words1 {
		set1[w] = true
	}

	matches := 0
	for _, w := range words2 {
		if set1[w] {
			matches++
		}
	}

	return float64(matches) / float64(max(len(words1), len(words2)))
}

// recordComment stores a generated comment in the history for an issue.
// This helps detect repetitive comments and maintains context for future generations.
// Limits history to maxHistoryPerIssue entries per issue to manage memory.
func (g *SmartCommentGenerator) recordComment(issueURL, comment string) {
	g.commentHistory[issueURL] = append(g.commentHistory[issueURL], comment)

	if len(g.commentHistory[issueURL]) > g.maxHistoryPerIssue {
		g.commentHistory[issueURL] = g.commentHistory[issueURL][len(g.commentHistory[issueURL])-g.maxHistoryPerIssue:]
	}
}

// improveComment enhances a low-quality comment by removing forbidden phrases,
// adding specific technical references from the extracted details, and normalizing
// whitespace to improve readability and quality score.
func (g *SmartCommentGenerator) improveComment(original string, details IssueDetails, issueType IssueType, extracted ExtractedDetails, quality CommentQualityResult) string {
	var improvements []string

	for _, issue := range quality.Issues {
		if strings.Contains(issue, "forbidden phrase") {
			for _, forbidden := range g.forbiddenPhrases {
				original = strings.ReplaceAll(original, forbidden, "")
			}
		}
	}

	if !quality.HasSpecificity {
		if len(extracted.Functions) > 0 {
			improvements = append(improvements, fmt.Sprintf("The issue in `%s()` function", extracted.Functions[0]))
		} else if len(extracted.Files) > 0 {
			improvements = append(improvements, fmt.Sprintf("Looking at %s", extracted.Files[0]))
		} else if len(extracted.Errors) > 0 {
			improvements = append(improvements, fmt.Sprintf("The error `%s` indicates", g.truncateError(extracted.Errors[0])))
		}
	}

	if len(improvements) > 0 {
		original = strings.Join(improvements, ". ") + ". " + original
	}

	original = regexp.MustCompile(`\s+`).ReplaceAllString(original, " ")
	original = strings.TrimSpace(original)

	return original
}

// generateQualitySuggestions creates actionable improvement suggestions based on
// quality assessment results. Suggests replacing generic phrases, adding specificity,
// and personalizing the comment to the issue.
func (g *SmartCommentGenerator) generateQualitySuggestions(result CommentQualityResult, details IssueDetails, extracted ExtractedDetails) []string {
	var suggestions []string

	if result.IsGeneric {
		suggestions = append(suggestions, "Replace generic phrases with specific technical references from the issue")
	}

	if !result.HasSpecificity {
		if len(extracted.Functions) > 0 {
			suggestions = append(suggestions, fmt.Sprintf("Mention the specific function: %s", extracted.Functions[0]))
		}
		if len(extracted.Files) > 0 {
			suggestions = append(suggestions, fmt.Sprintf("Reference the specific file: %s", extracted.Files[0]))
		}
		if len(extracted.Errors) > 0 {
			suggestions = append(suggestions, fmt.Sprintf("Discuss the error: %s", extracted.Errors[0]))
		}
	}

	if result.IsAISounding {
		suggestions = append(suggestions, "Add a personal observation or question specific to this issue")
	}

	return suggestions
}

// generateFeatureQuestions creates specific questions to clarify feature requirements
// such as API design, configuration options, and backward compatibility concerns.
func (g *SmartCommentGenerator) generateFeatureQuestions(details IssueDetails, extracted ExtractedDetails) []string {
	var questions []string

	titleLower := strings.ToLower(details.Title)
	bodyLower := strings.ToLower(details.Body)

	if !strings.Contains(bodyLower, "api") && !strings.Contains(bodyLower, "interface") {
		questions = append(questions, "Should this be exposed via a public API or remain internal?")
	}

	if strings.Contains(titleLower, "config") || strings.Contains(bodyLower, "config") {
		questions = append(questions, "What configuration options should be supported?")
	}

	if strings.Contains(bodyLower, "backward") || strings.Contains(bodyLower, "compat") {
		questions = append(questions, "Are there backward compatibility requirements?")
	}

	if len(questions) == 0 {
		questions = append(questions, "Are there any specific requirements or constraints for the implementation?")
		questions = append(questions, "Should this follow an existing pattern in the codebase?")
	}

	if len(questions) > 2 {
		questions = questions[:2]
	}

	return questions
}

// extractFeatureDescription extracts and normalizes a feature description from the title
// by removing prefixes like "Feature:", "feat:", "[Feature]" and truncating if too long.
func (g *SmartCommentGenerator) extractFeatureDescription(title string) string {
	title = strings.TrimSpace(title)

	title = strings.TrimPrefix(title, "Feature:")
	title = strings.TrimPrefix(title, "feat:")
	title = strings.TrimPrefix(title, "[Feature]")
	title = strings.TrimFunc(title, func(r rune) bool {
		return r == ' ' || r == ':'
	})

	words := strings.Fields(title)
	if len(words) > 6 {
		return strings.Join(words[:6], " ") + "..."
	}
	return title
}

// suggestApproach recommends an implementation strategy based on the issue content.
// For example: extending functions, adding struct fields, refactoring, or test-driven development.
func (g *SmartCommentGenerator) suggestApproach(details IssueDetails, extracted ExtractedDetails) string {
	titleLower := strings.ToLower(details.Title)
	bodyLower := strings.ToLower(details.Body)

	if len(extracted.Functions) > 0 {
		return fmt.Sprintf("Extend `%s()` with the new behavior while maintaining existing functionality", extracted.Functions[0])
	}

	if len(extracted.Structs) > 0 {
		return fmt.Sprintf("Add new fields/methods to `%s` struct with proper validation", extracted.Structs[0])
	}

	if strings.Contains(titleLower, "refactor") {
		return "Break down the changes into small, reviewable commits"
	}

	if strings.Contains(bodyLower, "test") {
		return "Implement the feature with comprehensive test coverage"
	}

	return "Start with a minimal implementation and iterate based on feedback"
}

// extractKeyNounPhrases extracts significant noun phrases from text by removing
// stop words and special characters. Returns up to 5 key phrases relevant to the issue.
func (g *SmartCommentGenerator) extractKeyNounPhrases(text string) []string {
	text = strings.ToLower(text)
	text = regexp.MustCompile(`[^a-z0-9\s\-]`).ReplaceAllString(text, " ")

	words := strings.Fields(text)

	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "must": true, "shall": true, "can": true,
		"need": true, "to": true, "of": true, "in": true, "for": true,
		"on": true, "with": true, "at": true, "by": true, "from": true,
		"as": true, "into": true, "through": true, "when": true, "where": true,
		"which": true, "this": true, "that": true, "these": true, "those": true,
		"add": true, "fix": true, "update": true, "remove": true, "change": true,
		"issue": true, "bug": true, "feature": true, "error": true, "not": true,
	}

	var phrases []string
	for _, word := range words {
		if !stopWords[word] && len(word) > 3 {
			phrases = append(phrases, word)
		}
	}

	if len(phrases) > 5 {
		phrases = phrases[:5]
	}

	return phrases
}

// truncateError shortens error messages to 50 characters for readability in comments.
func (g *SmartCommentGenerator) truncateError(err string) string {
	err = strings.TrimSpace(err)
	if len(err) > 50 {
		return err[:47] + "..."
	}
	return err
}

// truncatePhrase shortens phrases to 60 characters for readability in comments.
func (g *SmartCommentGenerator) truncatePhrase(phrase string) string {
	phrase = strings.TrimSpace(phrase)
	if len(phrase) > 60 {
		return phrase[:57] + "..."
	}
	return phrase
}

// GetCommentHistory returns the history of comments posted for a specific issue.
func (g *SmartCommentGenerator) GetCommentHistory(issueURL string) []string {
	return g.commentHistory[issueURL]
}

// SetMinQualityScore sets the minimum acceptable quality score (0.0-1.0).
// Comments below this threshold will be improved or rejected.
func (g *SmartCommentGenerator) SetMinQualityScore(score float64) {
	if score >= 0 && score <= 1 {
		g.minQualityScore = score
	}
}

// ClearHistory removes all stored comment history to reset the generator state.
func (g *SmartCommentGenerator) ClearHistory() {
	g.commentHistory = make(map[string][]string)
}

// GenerateSmartComment is a convenience function that creates a new generator and
// generates a smart comment for the given issue details in a single call.
func GenerateSmartComment(details IssueDetails) (*SmartComment, error) {
	generator := NewSmartCommentGenerator()
	return generator.GenerateSmartComment(details)
}
