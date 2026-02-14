package main

import (
	"fmt"
	"regexp"
	"strings"
)

type CommentQuality struct {
	Score        float64
	Issues       []string
	IsAISounding bool
	IsRelevant   bool
	IsGeneric    bool
	HasValue     bool
	SuggestedFix string
}

type CommentAnalyzer struct {
	issueTitle  string
	issueBody   string
	issueLabels []string
}

func NewCommentAnalyzer(title, body string, labels []string) *CommentAnalyzer {
	return &CommentAnalyzer{
		issueTitle:  title,
		issueBody:   body,
		issueLabels: labels,
	}
}

func (ca *CommentAnalyzer) AnalyzeComment(comment string) CommentQuality {
	quality := CommentQuality{
		Score:      1.0,
		Issues:     []string{},
		IsRelevant: true,
	}

	commentLower := strings.ToLower(comment)
	titleLower := strings.ToLower(ca.issueTitle)
	bodyLower := strings.ToLower(ca.issueBody)

	aiPhrases := []string{
		"i'd like to work on this",
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
	}

	genericCount := 0
	for _, phrase := range aiPhrases {
		if strings.Contains(commentLower, phrase) {
			genericCount++
		}
	}

	if genericCount >= 2 {
		quality.IsGeneric = true
		quality.IsAISounding = true
		quality.Score -= 0.4
		quality.Issues = append(quality.Issues, "Comment sounds generic/AI-generated (uses multiple template phrases)")
	}

	techWords := []string{"function", "method", "struct", "interface", "package", "file", "module", "error", "bug", "fix", "implement", "refactor", "test", "config", "api", "endpoint", "handler", "request", "crash", "nil"}
	issueTechWords := 0
	for _, word := range techWords {
		if strings.Contains(titleLower, word) || strings.Contains(bodyLower, word) {
			issueTechWords++
		}
	}

	commentTechWords := 0
	for _, word := range techWords {
		if strings.Contains(commentLower, word) {
			commentTechWords++
		}
	}

	if issueTechWords > 0 && commentTechWords == 0 {
		quality.IsRelevant = false
		quality.Score -= 0.3
		quality.Issues = append(quality.Issues, "Comment doesn't mention any technical details from the issue")
	}

	titleWords := extractKeywords(titleLower)
	commentWords := extractKeywords(commentLower)
	overlap := 0
	for _, tw := range titleWords {
		for _, cw := range commentWords {
			if tw == cw && len(tw) > 3 {
				overlap++
			}
		}
	}

	if overlap < 1 && commentTechWords == 0 {
		quality.IsRelevant = false
		quality.Score -= 0.2
		quality.Issues = append(quality.Issues, "Comment doesn't reference keywords from the issue title")
	}

	if strings.Contains(commentLower, "rfc") && !strings.Contains(titleLower, "rfc") && !strings.Contains(bodyLower, "rfc") {
		quality.Score -= 0.5
		quality.Issues = append(quality.Issues, "CRITICAL: Mentioning RFCs when issue doesn't reference them - appears unrelated/pretentious")
	}

	hasQuestion := strings.Contains(comment, "?")
	hasSpecific := strings.Contains(commentLower, "in ") ||
		strings.Contains(commentLower, "file ") ||
		strings.Contains(commentLower, "line ") ||
		strings.Contains(commentLower, "func ")
	hasApproach := strings.Contains(commentLower, "approach:") ||
		strings.Contains(commentLower, "plan:") ||
		strings.Contains(commentLower, "steps:")

	if hasSpecific || hasApproach {
		quality.HasValue = true
		quality.Score += 0.2
	} else if !hasQuestion {
		quality.Issues = append(quality.Issues, "Comment lacks specific details or questions - doesn't add value")
	}

	if strings.Contains(commentLower, "i'd like to") && !quality.HasValue {
		quality.Issues = append(quality.Issues, "CRITICAL: Generic 'I'd like to work on this' without showing understanding of the issue")
		quality.Score -= 0.3
	}

	if quality.Score < 0 {
		quality.Score = 0
	}
	if quality.Score > 1 {
		quality.Score = 1
	}

	quality.SuggestedFix = ca.suggestFix(quality)

	return quality
}

func (ca *CommentAnalyzer) suggestFix(quality CommentQuality) string {
	if len(quality.Issues) == 0 {
		return ""
	}

	var suggestions []string

	if quality.IsGeneric {
		suggestions = append(suggestions, "Instead of generic phrases, mention specific technical details from the issue")
	}

	if !quality.IsRelevant {
		suggestions = append(suggestions, "Reference specific functions, files, or concepts mentioned in the issue")
	}

	suggestions = append(suggestions, "Good comment example:")
	suggestions = append(suggestions, fmt.Sprintf("  'I see the issue is in %s. The problem seems to be...'", ca.getFirstTechWord()))

	return strings.Join(suggestions, "\n")
}

func (ca *CommentAnalyzer) getFirstTechWord() string {
	techWords := []string{"function", "method", "struct", "interface", "package", "file", "module", "error", "bug", "fix"}
	for _, word := range techWords {
		if strings.Contains(strings.ToLower(ca.issueTitle), word) {
			return word
		}
	}
	return "the code"
}

func extractKeywords(text string) []string {
	text = strings.ToLower(text)
	text = regexp.MustCompile(`[^a-z0-9\s]`).ReplaceAllString(text, " ")
	words := strings.Fields(text)

	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
		"were": true, "be": true, "been": true, "being": true, "have": true,
		"has": true, "had": true, "do": true, "does": true, "did": true,
		"will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "must": true, "shall": true,
		"can": true, "need": true, "dare": true, "ought": true,
		"used": true, "to": true, "of": true, "in": true, "for": true,
		"on": true, "with": true, "at": true, "by": true, "from": true,
		"as": true, "into": true, "through": true, "during": true,
		"before": true, "after": true, "above": true, "below": true,
		"between": true, "under": true, "again": true, "further": true,
		"then": true, "once": true, "here": true, "there": true,
		"when": true, "where": true, "why": true, "how": true,
		"all": true, "each": true, "few": true, "more": true,
		"most": true, "other": true, "some": true, "such": true,
		"no": true, "nor": true, "not": true, "only": true,
		"own": true, "same": true, "so": true, "than": true,
		"too": true, "very": true, "just": true, "and": true,
		"but": true, "if": true, "or": true, "because": true,
		"until": true, "while": true, "this": true, "that": true,
		"these": true, "those": true, "am": true, "i": true,
		"me": true, "my": true, "myself": true, "we": true,
		"our": true, "ours": true, "ourselves": true, "you": true,
		"your": true, "yours": true, "yourself": true, "yourselves": true,
		"he": true, "him": true, "his": true, "himself": true,
		"she": true, "her": true, "hers": true, "herself": true,
		"it": true, "its": true, "itself": true, "they": true,
		"them": true, "their": true, "theirs": true, "themselves": true,
		"what": true, "which": true, "who": true, "whom": true,
	}

	var keywords []string
	for _, word := range words {
		if !stopWords[word] && len(word) > 2 {
			keywords = append(keywords, word)
		}
	}
	return keywords
}

func GenerateGoodComment(title, body string, labels []string) string {
	analyzer := NewCommentAnalyzer(title, body, labels)

	titleLower := strings.ToLower(title)
	bodyLower := strings.ToLower(body)

	var comment strings.Builder

	if strings.Contains(titleLower, "doc") || strings.Contains(titleLower, "documentation") {
		comment.WriteString("I noticed the documentation issue in ")
		if strings.Contains(bodyLower, "file") {
			fileMatch := regexp.MustCompile(`file[:\s]+([^\s,]+)`).FindStringSubmatch(bodyLower)
			if len(fileMatch) > 1 {
				comment.WriteString(fileMatch[1])
			} else {
				comment.WriteString("the mentioned section")
			}
		} else {
			comment.WriteString("the docs")
		}
		comment.WriteString(". I can help update it.")
		return comment.String()
	}

	if strings.Contains(titleLower, "bug") {
		comment.WriteString("I see the bug occurs in ")
		if strings.Contains(bodyLower, "func") {
			funcMatch := regexp.MustCompile(`func[:\s]+([^\s(]+)`).FindStringSubmatch(bodyLower)
			if len(funcMatch) > 1 {
				comment.WriteString(funcMatch[1])
			}
		} else {
			comment.WriteString("the described scenario")
		}
		comment.WriteString(". Has this been reproduced consistently?")
		return comment.String()
	}

	for _, label := range labels {
		labelLower := strings.ToLower(label)
		if strings.Contains(labelLower, "good first issue") {
			comment.WriteString("This looks like a good starting point. ")
			comment.WriteString("I've read through the issue and understand the scope. ")
			comment.WriteString("Is there any additional context needed before starting?")
			return comment.String()
		}
		if strings.Contains(labelLower, "help wanted") {
			comment.WriteString("I have some ideas on how to approach this. ")
			comment.WriteString("Would you like me to outline a plan first, or should I open a draft PR?")
			return comment.String()
		}
	}

	if analyzer.analyzeIssueComplexity() == "simple" {
		comment.WriteString("I can take a look at this. ")
		comment.WriteString("From the description, it seems like a straightforward fix. ")
		comment.WriteString("Any specific requirements I should know about?")
	} else {
		comment.WriteString("This is an interesting issue. ")
		comment.WriteString("I'd like to understand more about the expected behavior before suggesting a solution.")
	}

	return comment.String()
}

func (ca *CommentAnalyzer) analyzeIssueComplexity() string {
	bodyLower := strings.ToLower(ca.issueBody)

	complexityIndicators := []string{
		"refactor", "architecture", "design", "migration", "breaking change",
		"performance", "security", "compatibility", "multiple", "several",
	}

	for _, indicator := range complexityIndicators {
		if strings.Contains(bodyLower, indicator) {
			return "complex"
		}
	}

	simpleIndicators := []string{
		"typo", "spelling", "missing", "incorrect", "update", "add", "remove",
		"documentation", "comment", "log", "message",
	}

	for _, indicator := range simpleIndicators {
		if strings.Contains(bodyLower, indicator) {
			return "simple"
		}
	}

	return "medium"
}
