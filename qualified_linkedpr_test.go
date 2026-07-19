package main

import (
	"strings"
	"testing"
	"time"
)

func TestLinkedPRVerdict(t *testing.T) {
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	recent := now.Add(-9 * 24 * time.Hour)  // within staleAttemptWindow (60d)
	old := now.Add(-120 * 24 * time.Hour)   // outside the window

	tests := []struct {
		name       string
		prs        []linkedPR
		wantBlock  bool
		wantReason string // substring; empty = no reason expected
	}{
		{
			name:       "no linked PRs is available",
			prs:        nil,
			wantBlock:  false,
		},
		{
			name:       "open linked PR blocks (taken)",
			prs:        []linkedPR{{Number: 42, State: "open"}},
			wantBlock:  true,
			wantReason: "open linked PR #42",
		},
		{
			name:       "recent closed unmerged PR blocks (stalled) — the keda#7691/#7713 case",
			prs:        []linkedPR{{Number: 7713, State: "closed", Merged: false, ClosedAt: recent}},
			wantBlock:  true,
			wantReason: "recent stalled PR #7713",
		},
		{
			name:      "old closed unmerged PR does not block",
			prs:       []linkedPR{{Number: 100, State: "closed", Merged: false, ClosedAt: old}},
			wantBlock: false,
		},
		{
			name:      "merged PR does not block even if recent",
			prs:       []linkedPR{{Number: 200, State: "closed", Merged: true, ClosedAt: recent}},
			wantBlock: false,
		},
		{
			name: "open wins over an old closed attempt",
			prs: []linkedPR{
				{Number: 300, State: "closed", Merged: false, ClosedAt: old},
				{Number: 301, State: "open"},
			},
			wantBlock:  true,
			wantReason: "open linked PR #301",
		},
		{
			name: "only merged + old closed → available",
			prs: []linkedPR{
				{Number: 400, State: "closed", Merged: true, ClosedAt: recent},
				{Number: 401, State: "closed", Merged: false, ClosedAt: old},
			},
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block, reason := linkedPRVerdict(tt.prs, now)
			if block != tt.wantBlock {
				t.Fatalf("linkedPRVerdict block = %v, want %v (reason %q)", block, tt.wantBlock, reason)
			}
			if tt.wantReason != "" && !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason = %q, want substring %q", reason, tt.wantReason)
			}
			if !tt.wantBlock && reason != "" {
				t.Errorf("expected empty reason when not blocking, got %q", reason)
			}
		})
	}
}
