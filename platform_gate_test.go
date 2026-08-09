package main

import (
	"testing"
	"time"

	"github.com/google/go-github/v58/github"
)

// scoreOf runs the real scorer over a synthetic issue so the gates are
// exercised exactly as the scan path would hit them.
func scoreOf(t *testing.T, title, body string, labels []string, author string) float64 {
	t.Helper()

	ghLabels := make([]*github.Label, 0, len(labels))
	for _, l := range labels {
		ghLabels = append(ghLabels, &github.Label{Name: github.String(l)})
	}
	comments := 0
	issue := &github.Issue{
		Title:     github.String(title),
		Body:      github.String(body),
		Labels:    ghLabels,
		Comments:  &comments,
		CreatedAt: &github.Timestamp{Time: time.Now().Add(-24 * time.Hour)},
		User:      &github.User{Login: github.String(author)},
	}
	return NewIssueScorer().ScoreIssue(issue, Project{
		Org: "test", Name: "test", Category: "Kubernetes", Stars: 20000,
	})
}

// The macOS podman issue was the highest-scoring alert of a whole batch (1.42)
// and cannot be reproduced on a Linux box, so it has to be disqualified rather
// than merely marked down.
func TestPlatformGateDropsOtherOSOnlyIssues(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		body   string
		labels []string
	}{
		{
			name:   "podman macOS build",
			title:  "Cannot build image on macOS through podman machine",
			body:   "Running podman machine on macOS 15, the build fails immediately.",
			labels: []string{"kind/bug", "remote", "macos", "machine"},
		},
		{
			name:  "darwin only",
			title: "GOOS=darwin: symlink resolution differs",
			body:  "On darwin the path is resolved through /private, so the check fails.",
		},
		{
			name:  "windows only",
			title: "Agent crashes on Windows",
			body:  "On windows server 2022 the service exits during startup. Repro in powershell.",
		},
		{
			name:   "label only",
			title:  "Volume mount path is wrong",
			body:   "The mount path ends up doubled.",
			labels: []string{"os/windows"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scoreOf(t, tt.title, tt.body, tt.labels, "someuser"); got >= 0 {
				t.Errorf("expected disqualification, got score %.2f", got)
			}
		})
	}
}

// The gate must not swallow ordinary Linux work. An issue that merely mentions
// macOS alongside Linux is still reproducible here, and monitoring issues talk
// about time windows without meaning the operating system.
func TestPlatformGateKeepsLinuxIssues(t *testing.T) {
	tests := []struct {
		name  string
		title string
		body  string
	}{
		{
			name:  "mentions macOS in passing",
			title: "Injector admission handler reads the full request body without a size limit",
			body:  "Reproduced on Linux with kubernetes 1.31, and also seen on macOS.",
		},
		{
			name:  "staleness windows, not the OS",
			title: "Staleness windows are computed twice per scrape",
			body:  "The lookback windows overlap, so linux hosts report double samples.",
		},
		{
			name:  "kubelet cgroup bug",
			title: "kubelet leaks a cgroup after pod deletion",
			body:  "On a bare metal cluster the cgroup directory survives the pod.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scoreOf(t, tt.title, tt.body, nil, "someuser"); got < 0 {
				t.Errorf("good Linux issue was disqualified (score %.2f)", got)
			}
		})
	}
}

// Eleven of twenty-eight alerts in one batch were bot-filed bookkeeping.
func TestTrackingIssueGate(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		body   string
		labels []string
		author string
		want   bool
	}{
		{
			name:   "watchflakes flake collector",
			title:  "go/types: TestSelf failures",
			body:   "#!watchflakes\ndefault <- pkg == \"go/types\"\nIssue created automatically to collect these failures.",
			labels: []string{"Automation"},
			author: "gopherbot",
			want:   true,
		},
		{
			name:   "backport tracker",
			title:  "cmd/compile: prove bug causes invalid indirect call [1.27 backport]",
			body:   "@mrkfrmn requested issue #80517 to be considered for backport to the next 1.27 minor release.",
			labels: []string{"CherryPickApproved", "compiler/runtime"},
			author: "gopherbot",
			want:   true,
		},
		{
			name:   "embargoed CVE stub",
			title:  "security: fix CVE-2026-56864",
			body:   "This is a PRIVATE issue for CVE-2026-56864, tracked in http://b/526586970.",
			labels: []string{"Security", "release-blocker"},
			author: "thatnealpatel",
			want:   true,
		},
		{
			name:   "bazel release fork",
			title:  "[9.3.0] Take the client's gRPC deadlines on the monotonic clock",
			body:   "Forked from #30596",
			author: "bazel-io",
			want:   true,
		},
		{
			name:   "app bot suffix",
			title:  "Bump some dependency",
			body:   "Automated update.",
			author: "renovate[bot]",
			want:   true,
		},
		{
			name:   "real human bug report",
			title:  "Injector admission handler reads the full request body without a size limit",
			body:   "The handler calls io.ReadAll on the request body with no limit.",
			labels: []string{"kind/bug"},
			author: "mehrdadbn9",
			want:   false,
		},
		{
			name:   "genuine security issue from a human",
			title:  "TLS verification is skipped when the CA bundle is empty",
			body:   "The client falls back to InsecureSkipVerify instead of failing closed.",
			labels: []string{"Security"},
			author: "someuser",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghLabels := make([]*github.Label, 0, len(tt.labels))
			for _, l := range tt.labels {
				ghLabels = append(ghLabels, &github.Label{Name: github.String(l)})
			}
			issue := &github.Issue{
				Title:  github.String(tt.title),
				Body:   github.String(tt.body),
				Labels: ghLabels,
				User:   &github.User{Login: github.String(tt.author)},
			}
			if got := isTrackingIssue(issue); got != tt.want {
				t.Errorf("isTrackingIssue = %v, want %v", got, tt.want)
			}
		})
	}
}

// golang/go filed 11 of 28 alerts in one night, which hid every other project.
func TestAlertsAreSpreadAcrossRepos(t *testing.T) {
	t.Setenv("MAX_ALERTS_PER_RUN", "6")
	t.Setenv("MAX_ALERTS_PER_REPO", "2")

	golang := Project{Org: "golang", Name: "go", Category: "Go", Stars: 125000}
	dapr := Project{Org: "dapr", Name: "dapr", Category: "Kubernetes", Stars: 24000}
	k9s := Project{Org: "derailed", Name: "k9s", Category: "Kubernetes", Stars: 24000}

	in := []Issue{
		{Project: golang, Number: 1, Score: 1.19},
		{Project: golang, Number: 2, Score: 1.14},
		{Project: golang, Number: 3, Score: 0.96},
		{Project: golang, Number: 4, Score: 0.78},
		{Project: golang, Number: 5, Score: 0.78},
		{Project: dapr, Number: 6, Score: 1.17},
		{Project: k9s, Number: 7, Score: 0.52},
	}

	out := filterAlertable(in)

	perRepo := map[string]int{}
	for _, iss := range out {
		perRepo[iss.Project.Org+"/"+iss.Project.Name]++
	}
	if perRepo["golang/go"] > 2 {
		t.Errorf("golang/go got %d alerts, want at most 2", perRepo["golang/go"])
	}
	if perRepo["dapr/dapr"] != 1 {
		t.Errorf("dapr/dapr got %d alerts, want 1", perRepo["dapr/dapr"])
	}
	// The low-scoring k9s issue must survive: it only lost to golang/go on
	// score, and holding golang/go back is exactly what frees the slot.
	if perRepo["derailed/k9s"] != 1 {
		t.Errorf("derailed/k9s got %d alerts, want 1", perRepo["derailed/k9s"])
	}
}
