package main

import "testing"

// ollama#17591 ("Windows: ollama create fails with 400 Bad Request") was sent
// as a "Strong claim" at score 1.03. Every Windows pattern in the gate needed
// a preposition or a suffix - "on windows", "windows server", "windows node" -
// and a bare title prefix matched none of them.
func TestMentionsWindowsOS(t *testing.T) {
	osSense := []string{
		"windows: ollama create fails with 400 bad request",
		"[windows] quadlet unit is not generated",
		"the agent crashes on windows and freebsd",
		"windows amd64 build is broken",
		"fails under windows subsystem",
	}
	for _, s := range osSense {
		if !mentionsWindowsOS(s) {
			t.Errorf("expected the OS sense, got none: %q", s)
		}
	}

	// The reason bare "windows" was never a token in the first place. Every one
	// of these is a monitoring issue worth working on.
	timeSense := []string{
		"staleness windows are not honoured by the query path",
		"support sliding windows in promtool",
		"compaction windows overlap after a restart",
		"scaling windows are ignored when the cooldown is zero",
		"retention windows should be configurable per tenant",
		"these windows are computed from the evaluation interval",
	}
	for _, s := range timeSense {
		if mentionsWindowsOS(s) {
			t.Errorf("false positive, a monitoring issue would be dropped: %q", s)
		}
	}
}

// The escape hatch has to keep working: a Linux issue that merely mentions
// Windows in passing is still worth doing.
func TestWindowsGateKeepsLinuxIssues(t *testing.T) {
	keep := scoreOf(t,
		"kubelet leaks cgroup mounts after pod eviction",
		"Reproduced on Linux with systemd cgroup v2. Also reported on Windows nodes.",
		nil, "someuser")
	if keep < 0 {
		t.Errorf("a Linux issue that mentions Windows in passing was disqualified (score %.2f)", keep)
	}

	drop := scoreOf(t,
		"Windows: ollama create fails with 400 Bad Request: invalid model name",
		"Running ollama create on my desktop returns 400 when the GGUF is referenced by path.",
		[]string{"bug"}, "someuser")
	if drop != -1 {
		t.Errorf("a Windows-only issue scored %.2f, want -1", drop)
	}
}

// The bare-metal gate the user asked to be certain of: no cloud account exists
// here, so a managed-cloud issue can never be verified.
func TestCloudGateDropsManagedCloudIssues(t *testing.T) {
	drop := []struct{ title, body string }{
		{"Nodes stuck NotReady after ASG scale-in", "Cluster runs on EKS 1.29."},
		{"CSI driver fails to attach EBS volume", "Volume stays in attaching state."},
		{"AKS upgrade leaves orphaned load balancer rules", "Seen on azure."},
		{"IAM role for service account is not honoured", "The pod cannot assume it."},
	}
	for _, tc := range drop {
		if got := scoreOf(t, tc.title, tc.body, nil, "someuser"); got != -1 {
			t.Errorf("cloud issue %q scored %.2f, want -1", tc.title, got)
		}
	}

	// Object storage is not a cloud dependency here: Thanos, Loki and Mimir all
	// run theirs against MinIO or Ceph on bare metal.
	keep := scoreOf(t,
		"compactor retries forever when the object store returns 503",
		"Reproduced against MinIO with an s3 backend on a local cluster.",
		nil, "someuser")
	if keep < 0 {
		t.Errorf("a MinIO/S3 bare-metal issue was disqualified (score %.2f)", keep)
	}
}
