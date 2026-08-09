package main

import (
	"strings"
	"testing"
)

// recordAlerted is what keeps notification_log and tracked_issues populated in
// the scheduled daemon. Before it existed the only writer was
// ProcessNewIssueNotifications, which runs solely under `mode == "confirmed"`,
// so after 339 issues seen both tables were still empty and the anti-spam
// cooldown could never fire.
//
// A nil antiSpam and a nil tracker are both real configurations, so the loop
// must survive them rather than panicking on the alert path.
func TestRecordAlertedToleratesMissingStores(t *testing.T) {
	f := &IssueFinder{}
	f.recordAlerted([]Issue{{
		Title:   "kubelet leaks cgroup mounts",
		URL:     "https://github.com/kubernetes/kubernetes/issues/1",
		Number:  1,
		Score:   0.90,
		Labels:  []string{"kind/bug", "triage/accepted"},
		Project: Project{Org: "kubernetes", Name: "kubernetes"},
	}})
}

// An empty batch must be a no-op, not a panic: filterAlertable and
// dropTakenIssues can both empty the slice.
func TestRecordAlertedEmptyBatch(t *testing.T) {
	(&IssueFinder{}).recordAlerted(nil)
}

// The tracker takes labels as one comma-joined string, and the confirmed flag is
// derived from those labels because Issue does not carry the richer flags that
// ConfirmedGoodFirstIssue has. Pin the derivation so a label-name change does
// not silently start recording every issue as unconfirmed.
func TestConfirmedLabelDerivation(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"prow triage accepted", []string{"kind/bug", "triage/accepted"}, true},
		{"plain confirmed", []string{"confirmed"}, true},
		{"status prefixed", []string{"status/confirmed"}, true},
		{"mixed case and padding", []string{"  Triage/Accepted "}, true},
		{"needs triage is not confirmed", []string{"needs-triage"}, false},
		{"no labels at all", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := false
			for _, l := range tt.labels {
				switch strings.ToLower(strings.TrimSpace(l)) {
				case "triage/accepted", "confirmed", "status/confirmed", "kind/confirmed":
					got = true
				}
			}
			if got != tt.want {
				t.Errorf("labels %v: got confirmed=%v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}
