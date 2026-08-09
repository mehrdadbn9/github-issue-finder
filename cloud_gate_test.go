package main

import "testing"

// Guards the bare-metal gate. The scorer disqualifies managed-cloud issues
// outright, so the matching has to be exact: a false positive here silently
// throws away a perfectly good issue.
func TestContainsAnyWordBoundaries(t *testing.T) {
	tokens := []string{"gcp", "gke", "gce", "aws", "ec2", "eks", "rds", "dynamodb",
		"azure", "aks", "cloudfront", "cloudwatch"}

	disqualify := []string{
		"deployment fails on eks 1.29",
		"aws load balancer controller drops targets",
		"panic when azure disk is detached",
		"[provider:aws,azure,gce] flaking test",
		"reconcile loop hammers ec2 describe calls",
	}
	for _, s := range disqualify {
		if !containsAnyWord(s, tokens) {
			t.Errorf("expected cloud match, got none: %q", s)
		}
	}

	// These are the substring traps. Under plain strings.Contains, "aks"
	// matches breaks/makes/takes/leaks and "eks" matches weeks, which would
	// disqualify unrelated bare-metal issues.
	keep := []string{
		"reconcile breaks when the host is detached",
		"the operator leaks goroutines on shutdown",
		"this makes the statefulset roll twice",
		"issue has been open for weeks with no triage",
		"ironic takes too long to power off a node",
		"standalone networking spec link is a 404",
	}
	for _, s := range keep {
		if containsAnyWord(s, tokens) {
			t.Errorf("false positive, issue would be wrongly disqualified: %q", s)
		}
	}
}

// The frontend gate must not eat Go operator issues. "react" is a normal
// English verb in reconciler-speak, so it is matched only as part of a
// frontend phrase (react-router, react component), never on its own.
func TestFrontendGateSpares(t *testing.T) {
	frontendPhrases := []string{
		"react-router", "react component", "reactjs", "react hook",
		"typescript", "javascript", "peer-dependency", "peer dependency",
		"storybook", "webpack", "eslint", "scss", "tailwind", "frontend",
		"front-end", "css class", "stylesheet",
	}
	shortTokens := []string{"npm", "yarn", "jsx", "tsx"}

	isFrontend := func(s string) bool {
		return containsAny(s, frontendPhrases) || containsAnyWord(s, shortTokens)
	}

	for _, s := range []string{
		"the reconciler should react to node changes",
		"controller does not react when the bmh is detached",
		"success rate drops during rollout",
		"add a test for the etcd storage access mode",
	} {
		if isFrontend(s) {
			t.Errorf("Go issue wrongly gated as frontend: %q", s)
		}
	}

	for _, s := range []string{
		"remove react-router-dom-v5-compat and v5 leftovers",
		"coordinate @grafana/scenes peer-dependency bump to react-router v7",
		"migrate the typescript build to webpack 5",
	} {
		if !isFrontend(s) {
			t.Errorf("frontend issue not gated: %q", s)
		}
	}
}

// Documents why containsAnyWord exists at all: the previous substring matcher
// really did match these.
func TestSubstringMatcherWasWrong(t *testing.T) {
	trap := []string{"aks", "eks"}
	for _, s := range []string{"reconcile breaks on detach", "open for weeks"} {
		if !containsAny(s, trap) {
			t.Errorf("expected the old substring matcher to (wrongly) match %q", s)
		}
		if containsAnyWord(s, trap) {
			t.Errorf("word matcher should not match %q", s)
		}
	}
}
