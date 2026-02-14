package main

import (
	"testing"
)

func TestAnalyzeComment_GenericAI(t *testing.T) {
	analyzer := NewCommentAnalyzer("Bug in HandleRequest function", "The HandleRequest function crashes when input is nil", []string{"bug"})

	comment := "I'd like to work on this. I can investigate and propose a solution. Please assign if appropriate."
	quality := analyzer.AnalyzeComment(comment)

	if !quality.IsGeneric {
		t.Error("Should detect generic AI-sounding comment")
	}
	if !quality.IsAISounding {
		t.Error("Should detect AI-sounding phrases")
	}
	if quality.Score >= 0.7 {
		t.Errorf("Score should be low for generic comment, got %.2f", quality.Score)
	}
}

func TestAnalyzeComment_UnrelatedRFC(t *testing.T) {
	analyzer := NewCommentAnalyzer("Fix typo in README", "There is a typo in the README file", []string{"documentation"})

	comment := "According to RFC 3986, the URL parsing should follow the specification. I'd like to implement this."
	quality := analyzer.AnalyzeComment(comment)

	if len(quality.Issues) == 0 {
		t.Error("Should detect unrelated RFC mention")
	}
	if quality.Score > 0.5 {
		t.Errorf("Score should be low for unrelated RFC, got %.2f", quality.Score)
	}
}

func TestAnalyzeComment_GoodComment(t *testing.T) {
	analyzer := NewCommentAnalyzer("Bug in HandleRequest function", "The HandleRequest function crashes when input is nil", []string{"bug"})

	comment := "I see the bug is in the HandleRequest function. The crash happens because nil check is missing. Should I add a nil check at line 42?"
	quality := analyzer.AnalyzeComment(comment)

	if quality.IsGeneric {
		t.Error("Should not detect as generic")
	}
	if !quality.IsRelevant {
		t.Error("Should detect as relevant")
	}
	if !quality.HasValue {
		t.Error("Should have value")
	}
	if quality.Score < 0.7 {
		t.Errorf("Score should be high for good comment, got %.2f", quality.Score)
	}
}

func TestAnalyzeComment_NoTechnicalMatch(t *testing.T) {
	analyzer := NewCommentAnalyzer("Fix buffer overflow in parseConfig", "The parseConfig function has a buffer overflow", []string{"bug", "security"})

	comment := "Hello, I would like to help with this."
	quality := analyzer.AnalyzeComment(comment)

	if quality.IsRelevant {
		t.Error("Should detect as not relevant - no technical terms matched")
	}
	if len(quality.Issues) == 0 {
		t.Error("Should have issues listed")
	}
}

func TestGenerateGoodComment_Documentation(t *testing.T) {
	title := "Update documentation for API endpoint"
	body := "The documentation at docs/api.md is outdated"
	labels := []string{"documentation"}

	comment := GenerateGoodComment(title, body, labels)

	if comment == "" {
		t.Error("Should generate a comment")
	}
	if containsGenericPhrase(comment) {
		t.Error("Should not contain generic AI phrases")
	}
}

func TestGenerateGoodComment_Bug(t *testing.T) {
	title := "Bug: Server crashes on startup"
	body := "The server crashes when started with --config flag"
	labels := []string{"bug"}

	comment := GenerateGoodComment(title, body, labels)

	if comment == "" {
		t.Error("Should generate a comment")
	}
}

func TestGenerateGoodComment_GoodFirstIssue(t *testing.T) {
	title := "Add unit test for utility function"
	body := "We need more test coverage"
	labels := []string{"good first issue", "help wanted"}

	comment := GenerateGoodComment(title, body, labels)

	if comment == "" {
		t.Error("Should generate a comment")
	}
	if containsGenericPhrase(comment) {
		t.Error("Should not be too generic")
	}
}

func containsGenericPhrase(comment string) bool {
	genericPhrases := []string{
		"I'd like to work on this issue. Please assign it to me if appropriate.",
		"I can investigate and propose a solution. Please assign if appropriate.",
	}
	for _, phrase := range genericPhrases {
		if comment == phrase {
			return true
		}
	}
	return false
}

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		text     string
		expected []string
	}{
		{"Fix bug in HandleRequest function", []string{"fix", "bug", "handlerequest", "function"}},
		{"The server is running", []string{"server", "running"}},
		{"", []string{}},
	}

	for _, tt := range tests {
		result := extractKeywords(tt.text)
		if len(result) != len(tt.expected) {
			t.Errorf("extractKeywords(%q) = %v, want %v", tt.text, result, tt.expected)
		}
	}
}
