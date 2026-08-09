package main

import "testing"

func TestConventionForSpecificity(t *testing.T) {
	m := LoadConventions()

	cases := []struct {
		owner, repo string
		want        ContributionConvention
		why         string
	}{
		// Org-wide Prow rules.
		{"kubernetes-sigs", "cluster-api", ConvProw, "org rule applies to any repo in the org"},
		{"metal3-io", "baremetal-operator", ConvProw, "metal3 runs prow: PR #3487 landed on needs-ok-to-test"},
		{"etcd-io", "etcd-operator", ConvProw, "etcd-io runs prow: PR #452 landed on needs-ok-to-test"},
		{"kubernetes", "kubernetes", ConvProw, "org rule"},

		// Exact repo entries.
		{"coredns", "coredns", ConvOpenPR, "direct PR accepted; #8316 went straight to PR"},
		{"argoproj", "argo-cd", ConvAskThenPR, "AGENTS.md wants an approved issue before a PR"},
		{"thanos-io", "thanos", ConvAskAssign, ""},

		// Unknown falls back to the safe option.
		{"someone", "random-repo", ConvUnknown, "unknown must not default to an aggressive action"},
	}

	for _, c := range cases {
		if got := conventionFor(m, c.owner, c.repo); got != c.want {
			t.Errorf("%s/%s: got %q want %q (%s)", c.owner, c.repo, got, c.want, c.why)
		}
	}
}

// An exact repo entry has to beat its own org-wide rule.
func TestExactRepoBeatsOrgWildcard(t *testing.T) {
	m := map[string]ContributionConvention{
		"acme/*":       ConvProw,
		"acme/special": ConvOpenPR,
	}
	if got := conventionFor(m, "acme", "special"); got != ConvOpenPR {
		t.Errorf("exact entry should win, got %q", got)
	}
	if got := conventionFor(m, "acme", "other"); got != ConvProw {
		t.Errorf("org rule should apply, got %q", got)
	}
}

// Every convention must produce guidance; an empty string in a Telegram
// message is a silent bug.
func TestConventionHelpNonEmpty(t *testing.T) {
	for _, c := range []ContributionConvention{
		ConvSelfAssign, ConvProw, ConvAskAssign, ConvOpenPR, ConvAskThenPR, ConvUnknown,
	} {
		if conventionHelp(c) == "" {
			t.Errorf("no help text for convention %q", c)
		}
	}
}
