package main

import (
	"testing"

	"github.com/google/go-github/v58/github"
)

func TestLooksLikeQuestion(t *testing.T) {
	f := &QualifiedIssueFinder{}

	tests := []struct {
		name  string
		title string
		body  string
		want  bool
	}{
		{
			name:  "how-to title is a question",
			title: "How do I configure TLS for the exporter?",
			body:  "I tried a few things and nothing works.",
			want:  true,
		},
		{
			name:  "is-it-possible title is a question",
			title: "Is it possible to reuse a WITH template across rules",
			body:  "Would be handy.",
			want:  true,
		},
		{
			name:  "support phrase in body vetoes despite feature-shaped title",
			title: "Add ability to scale shards",
			body:  "My question is whether the operator already does this somewhere.",
			want:  true,
		},
		{
			name:  "two soft phrases pile up",
			title: "Agent restarts unexpectedly",
			body:  "Any idea what causes this? Any help appreciated.",
			want:  true,
		},
		{
			name:  "question-mark title with no actionable signal",
			title: "Metrics missing after upgrade?",
			body:  "Just noticed this.",
			want:  true,
		},
		{
			name:  "question-mark title WITH code block is a real bug report",
			title: "Panic on nil selector?",
			body:  "Steps to reproduce:\n1. apply CR\n```\npanic: nil map\n```\nExpected: no panic",
			want:  false,
		},
		{
			name:  "genuine feature request passes",
			title: "Allow VMAlert CRD to store s3 credentials in a secret",
			body:  "Currently credentials are inline. Acceptance criteria:\n- [ ] secretRef field\n- [ ] docs",
			want:  false,
		},
		{
			name:  "genuine bug with repro passes",
			title: "lease comparison in txn returns wrong result",
			body:  "Steps to reproduce:\n1. create lease\n```go\ntxn.If(...)\n```\nExpected: true, Actual: false",
			want:  false,
		},
		{
			name:  "single soft phrase alone does not veto",
			title: "Improve rendering of unschedulable nodes",
			body:  "Any help would speed this up. Here is the plan: refactor the node view.",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &github.Issue{
				Title: github.String(tt.title),
				Body:  github.String(tt.body),
			}
			if got := f.looksLikeQuestion(issue); got != tt.want {
				t.Errorf("looksLikeQuestion(%q) = %v, want %v", tt.title, got, tt.want)
			}
		})
	}
}
