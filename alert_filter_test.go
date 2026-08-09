package main

import (
	"fmt"
	"testing"
)

// The scorer returning a negative number is not, on its own, a filter.
// kubernetes#141200 scored -1.00 and was still delivered to Telegram because
// nothing dropped it before the send.
func TestFilterAlertableDropsDisqualified(t *testing.T) {
	t.Setenv("MIN_ALERT_SCORE", "0.50")

	in := []Issue{
		{Title: "[Feature:GPUDevicePlugin] sanity test", Score: -1.00},
		{Title: "grafana react-router v7 upgrade", Score: -1.00},
		{Title: "weak but valid", Score: 0.31},
		{Title: "solid bare-metal operator bug", Score: 0.82},
		{Title: "exactly at the floor", Score: 0.50},
	}

	got := filterAlertable(in)

	if len(got) != 2 {
		t.Fatalf("expected 2 alertable, got %d: %+v", len(got), titles(got))
	}
	for _, iss := range got {
		if iss.Score < 0 {
			t.Errorf("disqualified issue survived the filter: %q (%.2f)", iss.Title, iss.Score)
		}
	}
	if got[0].Title != "solid bare-metal operator bug" || got[1].Title != "exactly at the floor" {
		t.Errorf("unexpected survivors: %v", titles(got))
	}
}

func TestMinAlertScoreOverride(t *testing.T) {
	t.Setenv("MIN_ALERT_SCORE", "0.80")
	if got := minAlertScore(); got != 0.80 {
		t.Errorf("override ignored, got %.2f", got)
	}

	t.Setenv("MIN_ALERT_SCORE", "not-a-number")
	if got := minAlertScore(); got != 0.50 {
		t.Errorf("bad value should fall back to 0.50, got %.2f", got)
	}
}

func titles(in []Issue) []string {
	out := make([]string, 0, len(in))
	for _, i := range in {
		out = append(out, i.Title)
	}
	return out
}

// The ML/AI projects outrank every CNCF project by stars, and only the top 30
// are scanned per run, so leaving them in meant Python repos crowded out the
// Go/DevOps ones regardless of scoring.
func TestExcludeCategoriesDropsMLAI(t *testing.T) {
	in := []Project{
		{Org: "huggingface", Name: "transformers", Category: "ML/AI", Stars: 140000},
		{Org: "langchain-ai", Name: "langchain", Category: "ML/AI", Stars: 100000},
		{Org: "etcd-io", Name: "etcd", Category: "Kubernetes", Stars: 50000},
		{Org: "metal3-io", Name: "baremetal-operator", Category: "Baremetal", Stars: 900},
	}
	got := excludeCategories(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 kept, got %d", len(got))
	}
	for _, p := range got {
		if p.Category == "ML/AI" {
			t.Errorf("ML/AI project survived: %s/%s", p.Org, p.Name)
		}
	}
}

func TestExcludeCategoriesOverride(t *testing.T) {
	in := []Project{{Org: "a", Name: "b", Category: "ML/AI"}}
	t.Setenv("SCAN_CATEGORIES_EXCLUDE", "")
	if got := excludeCategories(in); len(got) != 1 {
		t.Errorf("empty override should disable filtering, got %d", len(got))
	}
}

// A single cycle must never fire dozens of Telegram messages.
func TestFilterAlertableCapsBurst(t *testing.T) {
	t.Setenv("MIN_ALERT_SCORE", "0.50")
	t.Setenv("MAX_ALERTS_PER_RUN", "6")

	// One issue per repo, so this exercises the total cap on its own without
	// the per-repo spread taking issues out first.
	in := make([]Issue, 0, 49)
	for i := 0; i < 49; i++ {
		in = append(in, Issue{
			Title:   "issue",
			Score:   0.50 + float64(i)/100,
			Project: Project{Org: "org", Name: fmt.Sprintf("repo%d", i)},
		})
	}
	got := filterAlertable(in)
	if len(got) != 6 {
		t.Fatalf("expected cap of 6, got %d", len(got))
	}
	// Highest scores must win, and be ordered.
	for i := 1; i < len(got); i++ {
		if got[i-1].Score < got[i].Score {
			t.Errorf("not sorted by score desc at %d", i)
		}
	}
	if got[0].Score < 0.95 {
		t.Errorf("cap should keep the best, top score was %.2f", got[0].Score)
	}
}
