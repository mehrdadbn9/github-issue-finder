package main

import "testing"

// Replays the real 20:40/20:41 Telegram batch. Before this logic almost every
// one of them collapsed to "open a PR that solves it", which is one button for
// all repos - the opposite of picking the right action per issue.
func TestDecideStepRealBatch(t *testing.T) {
	m := LoadConventions()

	cases := []struct {
		name    string
		owner   string
		repo    string
		title   string
		labels  []string
		want    NextStep
		wantWhy string
	}{
		{
			name: "ingress-nginx untriaged bug", owner: "kubernetes", repo: "ingress-nginx",
			title:   "When an Ingress resource is deleted, its metrics remain in Prometheus",
			labels:  []string{"kind/bug", "needs-triage", "needs-priority"},
			want:    StepTriage,
			wantWhy: "prow repo, but nobody has triaged it - /assign would be a land grab",
		},
		{
			name: "watchtower explicitly available", owner: "containrrr", repo: "watchtower",
			title:   "Enabling the HTTP API enables a debug endpoint (/debug/vars)",
			labels:  []string{"Type: Bug", "Priority: Medium", "Status: Available"},
			want:    StepPR,
			wantWhy: "project marks it Available - just fix it",
		},
		{
			name: "kubectx pending repro", owner: "ahmetb", repo: "kubectx",
			title:   "kubens 0.11.0 on MacOS does not change the active namespace",
			labels:  []string{"Bug", "Pending repro"},
			want:    StepRepro,
			wantWhy: "maintainer is waiting on a reproduction",
		},
		{
			name: "teleport design issue", owner: "gravitational", repo: "teleport",
			title:   "Scoped Web UI (Design)",
			labels:  []string{},
			want:    StepDiscuss,
			wantWhy: "design work - a surprise PR gets closed",
		},
		{
			name: "fasthttp plain bug", owner: "valyala", repo: "fasthttp",
			title:   "A big MaxIdleConnDuration value results in zombie goroutines",
			labels:  []string{"bug"},
			want:    StepPR,
			wantWhy: "confirmed bug, no claim ritual in this repo",
		},
		{
			name: "metal3 good first issue", owner: "metal3-io", repo: "baremetal-operator",
			title:   "Fix a broken link",
			labels:  []string{"good first issue", "help wanted", "triage/accepted"},
			want:    StepProwAssign,
			wantWhy: "claimable + prow org -> /assign is the sanctioned claim",
		},
		{
			name: "thanos ask-assign repo, trusted", owner: "thanos-io", repo: "thanos",
			title:   "dedup drops the latest point",
			labels:  []string{"bug"},
			want:    StepAsk,
			wantWhy: "convention is ask-assign and trust is not zero",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conv := conventionFor(m, c.owner, c.repo)
			iss := Issue{Title: c.title, Labels: c.labels}
			got := decideStep(conv, iss, false)
			if got != c.want {
				t.Errorf("got %q want %q (%s)", got, c.want, c.wantWhy)
			}
		})
	}
}

// A claimable label must beat the untriaged guard: "good first issue" plus
// "needs-triage" still means the project wants help.
func TestClaimableBeatsTriageGuard(t *testing.T) {
	iss := Issue{Title: "x", Labels: []string{"good first issue", "needs-triage"}}
	if got := decideStep(ConvProw, iss, false); got != StepProwAssign {
		t.Errorf("claimable should win, got %q", got)
	}
}

// At zero trust an ask-convention becomes a PR, but only when the issue is
// actually actionable - it must not override the triage/repro guards.
func TestContributeFirstDoesNotOverrideGuards(t *testing.T) {
	untriaged := Issue{Title: "x", Labels: []string{"needs-triage"}}
	if got := decideStep(ConvUnknown, untriaged, true); got != StepTriage {
		t.Errorf("contributeFirst must not push a PR on an untriaged issue, got %q", got)
	}

	ready := Issue{Title: "x", Labels: []string{"bug"}}
	if got := decideStep(ConvUnknown, ready, true); got != StepPR {
		t.Errorf("at zero trust a ready issue should suggest a PR, got %q", got)
	}
}

// Every step needs a distinct button and a callback verb the poller handles.
func TestStepButtonsDistinct(t *testing.T) {
	handled := map[string]bool{
		"prow": true, "assign": true, "ask": true,
		"pr": true, "repro": true, "triage": true, "discuss": true,
	}
	seen := map[string]bool{}
	for _, s := range []NextStep{
		StepProwAssign, StepSelfAssign, StepAsk, StepPR,
		StepRepro, StepTriage, StepDiscuss,
	} {
		label, verb := stepButton(s)
		if label == "" {
			t.Errorf("step %q has no label", s)
		}
		if !handled[verb] {
			t.Errorf("step %q emits verb %q which handleCallback does not handle", s, verb)
		}
		if seen[label] {
			t.Errorf("duplicate button label %q", label)
		}
		seen[label] = true
	}
}
