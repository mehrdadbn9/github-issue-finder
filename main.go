package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/go-github/v58/github"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"golang.org/x/oauth2"
)

type Project struct {
	Org      string
	Name     string
	Category string
	Stars    int
}

type Issue struct {
	Project     Project
	Title       string
	URL         string
	Number      int
	Score       float64
	CreatedAt   time.Time
	Comments    int
	Labels      []string
	Language    string
	IsGoodFirst bool
}

type IssueFilter struct {
	MinScore      float64
	MaxScore      float64
	Categories    []string
	MaxComments   int
	MinStars      int
	Labels        []string
	ExcludeLabels []string
	CreatedAfter  time.Time
	CreatedBefore time.Time
}

type IssueResult struct {
	Issues     []Issue
	TotalCount int
	Duration   time.Duration
	Error      error
}

type IssueScorer struct {
	weights map[string]float64
}

type RateLimitStatus struct {
	Limit     int
	Remaining int
	Reset     time.Time
}

type RateLimiter struct {
	mu           sync.Mutex
	status       RateLimitStatus
	client       *github.Client
	minRemaining int
}

func NewRateLimiter(client *github.Client, minRemaining int) *RateLimiter {
	return &RateLimiter{
		client:       client,
		minRemaining: minRemaining,
		status: RateLimitStatus{
			Limit:     5000,
			Remaining: 5000,
			Reset:     time.Now().Add(time.Hour),
		},
	}
}

func (r *RateLimiter) updateStatusFromResponse(resp *github.Response) {
	if resp == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if limit := resp.Rate.Limit; limit != 0 {
		r.status.Limit = limit
	}
	if remaining := resp.Rate.Remaining; remaining != 0 {
		r.status.Remaining = remaining
	}
	if !resp.Rate.Reset.IsZero() {
		r.status.Reset = resp.Rate.Reset.Time
	}

	log.Printf("[Rate Limit] %d/%d remaining, reset at %v",
		r.status.Remaining, r.status.Limit, r.status.Reset.Format("15:04:05"))
}

func (r *RateLimiter) WaitIfNeeded(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.status.Remaining < r.minRemaining {
		waitDuration := time.Until(r.status.Reset)
		if waitDuration < 0 {
			waitDuration = time.Minute
		}

		log.Printf("[Rate Limit] Near limit (%d/%d remaining), waiting %v until reset at %v",
			r.status.Remaining, r.status.Limit, waitDuration.Round(time.Second), r.status.Reset.Format("15:04:05"))

		r.mu.Unlock()
		select {
		case <-ctx.Done():
			r.mu.Lock()
			return ctx.Err()
		case <-time.After(waitDuration):
			r.mu.Lock()
		}

		r.status.Remaining = r.status.Limit
	}

	return nil
}

func (r *RateLimiter) checkRateLimit(ctx context.Context) error {
	rateLimit, _, err := r.client.RateLimits(ctx)
	if err != nil {
		log.Printf("[Rate Limit] Failed to fetch rate limits: %v", err)
		return err
	}

	if core := rateLimit.Core; core != nil {
		r.mu.Lock()
		r.status.Limit = core.Limit
		r.status.Remaining = core.Remaining
		if !core.Reset.IsZero() {
			r.status.Reset = core.Reset.Time
		}
		r.mu.Unlock()

		log.Printf("[Rate Limit] Current status: %d/%d remaining, reset at %v",
			r.status.Remaining, r.status.Limit, r.status.Reset.Format("15:04:05"))
	}

	return nil
}

func (r *RateLimiter) executeWithRetry(ctx context.Context, operation string, fn func() (*github.Response, error)) error {
	maxRetries := 5
	backoff := time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := r.WaitIfNeeded(ctx); err != nil {
			return err
		}

		resp, err := fn()
		if err != nil {
			if resp != nil {
				r.updateStatusFromResponse(resp)
			}

			if strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "rate limit") {
				if attempt < maxRetries {
					waitTime := backoff * time.Duration(1<<(attempt-1))
					log.Printf("[Rate Limit] Hit rate limit on attempt %d/%d for %s, retrying in %v",
						attempt, maxRetries, operation, waitTime.Round(time.Second))

					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(waitTime):
						continue
					}
				}
			}

			return fmt.Errorf("failed after %d attempts: %w", attempt, err)
		}

		if resp != nil {
			r.updateStatusFromResponse(resp)
		}

		return nil
	}

	return fmt.Errorf("max retries exceeded for %s", operation)
}

func NewIssueScorer() *IssueScorer {
	return &IssueScorer{
		weights: map[string]float64{
			"stars_factor":      0.10,
			"comments_factor":   0.25,
			"recency_factor":    0.25,
			"labels_factor":     0.25,
			"difficulty_factor": 0.15,
		},
	}
}

func (s *IssueScorer) ScoreIssue(issue *github.Issue, project Project) float64 {
	var score float64

	starsScore := s.normalizeStars(project.Stars)
	score += starsScore * s.weights["stars_factor"]

	commentsScore := s.normalizeComments(*issue.Comments)
	score += commentsScore * s.weights["comments_factor"]

	recencyScore := s.normalizeRecency(issue.CreatedAt.Time)
	score += recencyScore * s.weights["recency_factor"]

	labelsScore := s.normalizeLabels(issue.Labels)
	score += labelsScore * s.weights["labels_factor"]

	difficultyScore := s.normalizeDifficulty(issue.Labels, safeString(issue.Body))
	score += difficultyScore * s.weights["difficulty_factor"]

	title := strings.ToLower(safeString(issue.Title))
	body := strings.ToLower(safeString(issue.Body))
	combined := title + " " + body

	// Go 1.26 related issues - high priority
	if strings.Contains(combined, "go 1.26") || strings.Contains(combined, "go1.26") || strings.Contains(combined, "golang 1.26") {
		score += 0.30
	}
	if strings.Contains(combined, "upgrade") && (strings.Contains(combined, "go ") || strings.Contains(combined, "golang")) {
		score += 0.15
	}

	// Good labels
	if strings.Contains(combined, "good first issue") || hasLabel(issue.Labels, "good first issue") {
		score += 0.20
	}
	if strings.Contains(combined, "help wanted") || hasLabel(issue.Labels, "help wanted") {
		score += 0.15
	}

	// TLS/Security - user preference
	if strings.Contains(strings.ToLower(project.Category), "tls") || strings.Contains(strings.ToLower(project.Category), "security") {
		score += 0.10
	}
	if strings.Contains(combined, "tls") || strings.Contains(combined, "ssl") || strings.Contains(combined, "certificate") || strings.Contains(combined, "https") {
		score += 0.10
	}

	// CNCF projects bonus - expanded list
	cncfProjects := []string{
		"kubernetes", "prometheus", "etcd", "istio", "cilium", "containerd", "grpc",
		"helm", "dapr", "keda", "argo", "rancher", "velero", "traefik", "flux",
		"knative", "opa", "cni", "cri-o", "runc", "coredns", "envoy", "linkerd",
		"crossplane", "keptn", "openfeature", "backstage", "dragonfly", "vineyard",
		"kubespray", "kubeadm", "minikube", "kind", "calico", "flannel", "rook",
		"longhorn", "openebs", "ceph", "minio", "kuma", "thanos", "victoriametrics",
	}
	if slices.ContainsFunc(cncfProjects, func(p string) bool {
		return strings.Contains(strings.ToLower(project.Name), p)
	}) {
		score += 0.15
	}

	// Learning-focused bonuses
	// Good first issue - best for learning
	if hasLabel(issue.Labels, "good first issue") || hasLabel(issue.Labels, "good-first-issue") {
		score += 0.25
	}

	// Help wanted - maintainers actively seeking contributors
	if hasLabel(issue.Labels, "help wanted") || hasLabel(issue.Labels, "help-wanted") {
		score += 0.20
	}

	// Beginner-friendly labels
	beginnerLabels := []string{"beginner", "starter", "easy", "newcomer", "first-timers-only"}
	for _, label := range beginnerLabels {
		if hasLabel(issue.Labels, label) {
			score += 0.15
			break
		}
	}

	// Documentation-only issues - easier to contribute
	if strings.Contains(combined, "documentation") || strings.Contains(combined, "docs") ||
		strings.Contains(title, "doc:") || hasLabel(issue.Labels, "documentation") {
		score += 0.15
	}

	// Clear scope indicators - issue mentions specific files/functions
	clearScopeKeywords := []string{"file:", "func:", "in ", "method", "struct", "interface", "package"}
	clearCount := 0
	for _, kw := range clearScopeKeywords {
		if strings.Contains(combined, kw) {
			clearCount++
		}
	}
	if clearCount >= 2 {
		score += 0.10
	}

	// Clear reproduction steps - issues with code blocks or steps
	if strings.Contains(body, "```") || strings.Contains(body, "steps to reproduce") ||
		strings.Contains(body, "reproduc") {
		score += 0.10
	}

	// Easy/quick fix indicators
	easyKeywords := []string{"quick", "easy", "simple", "trivial", "small", "minor", "typo", "spelling"}
	if containsAny(combined, easyKeywords) {
		score += 0.05
	}

	// Stale but available - issues open for a while with no activity (1-6 months)
	age := time.Since(issue.CreatedAt.Time).Hours()
	if age > 720 && age < 4320 && *issue.Comments <= 3 {
		score += 0.10
	}

	// Hard bare-metal gate. There is no cloud account here, so a managed-cloud
	// issue cannot be reproduced or verified locally no matter how good it
	// looks - it is disqualified outright rather than merely marked down. A
	// -0.50 penalty was not enough: k8s#141198
	// ("[Provider:aws,azure,gce]") still scored 0.28 and got alerted.
	//
	// Short tokens are matched on word boundaries on purpose. As plain
	// substrings "aks" hits breaks/makes/takes, "eks" hits weeks, and "rds"
	// hits words - which would silently disqualify unrelated issues.
	// Deliberately NOT excluded: "openstack" and "ironic", which are how
	// metal3/bare-metal provisioning is described.
	cloudPhrases := []string{
		"google cloud", "compute engine", "cloud sql", "bigquery", "pubsub",
		"amazon web", "s3 bucket", "microsoft azure", "azure functions",
		"azure storage", "cloud provider", "cloudprovider",
		"cloudformation", "arm template", "azure blob", "aws sdk", "boto3",
		"workload identity", "managed identity",
	}
	// Deliberately NOT tokens: "s3" and "gcs". Thanos, Loki and Mimir all run
	// their object store against MinIO or Ceph on bare metal, so those two
	// would throw away the whole observability backlog.
	cloudTokens := []string{
		"gcp", "gke", "gce", "aws", "ec2", "eks", "rds", "dynamodb",
		"azure", "aks", "cloudfront", "cloudwatch",
		"fargate", "route53", "elb", "alb", "nlb", "ebs", "efs", "iam",
		"azurerm",
	}
	if containsAny(combined, cloudPhrases) || containsAnyWord(combined, cloudTokens) {
		return -1
	}

	if hasAnyLabel(issue.Labels, "provider:google", "provider:aws", "provider:azure",
		"area/gcp", "area/aws", "area/azure", "area/provider/aws", "area/provider/azure",
		"area/provider/gcp") {
		return -1
	}

	// Frontend/JS gate. This is a Go/DevOps track, so a React or CSS task is
	// not a fit no matter how well scored: one run alerted seven consecutive
	// grafana react-router issues (#130187-#130194).
	if hasAnyLabel(issue.Labels, "area/frontend", "area/frontend-platform", "javascript",
		"typescript", "area/ui", "ui/ux", "area/design-system") {
		return -1
	}
	// Phrases only, no bare "react": a controller issue legitimately says
	// "the reconciler should react to node changes", and "ui" appears inside
	// build/guide text. Same trap as "aks" matching "breaks".
	frontendPhrases := []string{
		"react-router", "react component", "reactjs", "react hook",
		"typescript", "javascript", "peer-dependency", "peer dependency",
		"storybook", "webpack", "eslint", "scss", "tailwind", "frontend",
		"front-end", "css class", "stylesheet",
	}
	if containsAny(combined, frontendPhrases) || containsAnyWord(combined, []string{"npm", "yarn", "jsx", "tsx"}) {
		return -1
	}

	// "internal" means the project's own team is doing it; outside PRs are not
	// wanted. Same for issues explicitly parked or already owned.
	if hasAnyLabel(issue.Labels, "internal", "wontfix", "invalid", "duplicate",
		"lifecycle/frozen", "do-not-merge") {
		return -1
	}

	// Platform gate. This is a Linux bare-metal box: a darwin or Windows bug
	// cannot be reproduced or verified here. podman#29396 ("Cannot build image
	// on macOS through podman machine") was the top alert of a whole batch at
	// score 1.42 and there is no way to work on it.
	//
	// Only disqualify when the issue is *exclusively* about those platforms.
	// Plenty of good Linux issues mention macOS in passing ("also seen on
	// macOS"), so a Linux signal anywhere in the text keeps the issue alive.
	//
	// Bare "windows" cannot be a plain token either, because monitoring issues
	// talk about staleness windows and sliding windows. Same trap as "aks" in
	// "breaks", so it gets its own preceding-word check below.
	if hasAnyLabel(issue.Labels, "os/macos", "os/windows", "platform/mac",
		"platform/windows", "area/windows", "kind/windows", "macos", "windows",
		"os: mac", "os-darwin") {
		return -1
	}
	otherOSPhrases := []string{
		"macos", "mac os", "osx", "os x", "apple silicon", "macbook",
		"podman machine", "docker desktop", "homebrew", "xcode",
		"on windows", "windows server", "windows container", "windows node",
		"powershell", "wsl2", "wsl 2", "windows 10", "windows 11",
		"%userprofile%", "%appdata%", "mingw", "msvc", "cmd.exe",
	}
	otherOSTokens := []string{"darwin", "win32", "win64"}
	linuxTokens := []string{
		"linux", "ubuntu", "debian", "fedora", "rhel", "centos", "alpine",
		"systemd", "kubernetes", "kubelet", "containerd", "cgroup", "iptables",
	}
	if (containsAny(combined, otherOSPhrases) || containsAnyWord(combined, otherOSTokens) ||
		mentionsWindowsOS(combined)) &&
		!containsAnyWord(combined, linuxTokens) {
		return -1
	}

	// Language gate. An issue written in a script Mehrdad cannot read is not
	// workable no matter how good it scores: an ollama Gemini Pro bug filed
	// entirely in Chinese went out as an alert. Adding the AI-Infra projects
	// made this common, since ollama and llama.cpp draw a large Chinese-speaking
	// user base.
	if mostlyNonLatin(combined) {
		return -1
	}

	// Bot-filed tracking issues are not contributions. golang/go alone took 11
	// of 28 alerts in one batch this way: gopherbot opens watchflakes flake
	// collectors ("Automation"), gopherbot opens backport trackers
	// ("CherryPickApproved"), and bazel-io forks release issues from the
	// internal fork ("Forked from #30596"). None of them are claimable by an
	// outside contributor - the flake collectors carry no reproducible failure
	// and backports are release-team mechanics.
	if isTrackingIssue(issue) {
		return -1
	}

	// Needs triage penalty - can't work on until triaged
	if hasLabel(issue.Labels, "needs-triage") {
		score -= 0.15
	}

	// Blocked/waiting penalty
	blockedKeywords := []string{"blocked", "waiting for", "needs approval", "on hold", "pending"}
	if containsAny(combined, blockedKeywords) {
		score -= 0.20
	}

	// Wontfix/invalid penalty
	if hasAnyLabel(issue.Labels, "wontfix", "invalid", "duplicate", "wont-fix") {
		score -= 0.50
	}

	// Needs info penalty - incomplete issue
	if hasAnyLabel(issue.Labels, "needs info", "needs-information", "waitingforinfo") {
		score -= 0.15
	}

	// Clamp score
	if score > 1.5 {
		score = 1.5
	}
	if score < 0 {
		score = 0
	}

	return score
}

func hasLabel(labels []*github.Label, target string) bool {
	targetLower := strings.ToLower(target)
	return slices.ContainsFunc(labels, func(label *github.Label) bool {
		return strings.Contains(strings.ToLower(label.GetName()), targetLower)
	})
}

func hasAnyLabel(labels []*github.Label, targets ...string) bool {
	return slices.ContainsFunc(targets, func(target string) bool {
		return hasLabel(labels, target)
	})
}

func hasConfirmedLabel(labels []*github.Label) bool {
	confirmedLabels := []string{"confirmed", "triage/accepted", "triage accepted", "accepted", "status/confirmed", "status/accepted", "lifecycle/confirmed"}
	for _, label := range labels {
		labelName := strings.ToLower(label.GetName())
		for _, confirmedLabel := range confirmedLabels {
			if strings.Contains(labelName, strings.ToLower(confirmedLabel)) {
				return true
			}
		}
	}
	return false
}

func hasGoodFirstIssueLabel(labels []*github.Label) bool {
	goodFirstLabels := []string{"good first issue", "good-first-issue", "first timers only", "first-timers-only", "beginner friendly"}
	for _, label := range labels {
		labelName := strings.ToLower(label.GetName())
		for _, gfiLabel := range goodFirstLabels {
			if strings.Contains(labelName, strings.ToLower(gfiLabel)) {
				return true
			}
		}
	}
	return false
}

func convertLabels(labelNames []string) []*github.Label {
	labels := make([]*github.Label, len(labelNames))
	for i, name := range labelNames {
		labels[i] = &github.Label{Name: github.String(name)}
	}
	return labels
}

func containsAny(text string, keywords []string) bool {
	return slices.ContainsFunc(keywords, func(kw string) bool {
		return strings.Contains(text, kw)
	})
}

// archivedCache memoises the archived flag per repo. The answer effectively
// never changes during a process lifetime, and this runs once per repo per
// scan, so caching keeps it to a single extra API call per repo.
var (
	archivedMu    sync.Mutex
	archivedCache = map[string]bool{}
)

// isArchived reports whether owner/repo is archived (read-only). On any API
// error it returns false: a transient failure must not silently hide a repo.
func (f *IssueFinder) isArchived(ctx context.Context, owner, name string) bool {
	key := owner + "/" + name

	archivedMu.Lock()
	if v, ok := archivedCache[key]; ok {
		archivedMu.Unlock()
		return v
	}
	archivedMu.Unlock()

	repo, _, err := f.client.Repositories.Get(ctx, owner, name)
	if err != nil {
		log.Printf("[Archived] could not check %s: %v (assuming active)", key, err)
		return false
	}
	v := repo.GetArchived() || repo.GetDisabled()

	archivedMu.Lock()
	archivedCache[key] = v
	archivedMu.Unlock()
	return v
}

// minAlertScore is the floor for sending an alert at all. Overridable with
// MIN_ALERT_SCORE. Anything the scorer disqualified is negative and is always
// dropped regardless of this value.
func minAlertScore() float64 {
	if raw := strings.TrimSpace(os.Getenv("MIN_ALERT_SCORE")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
	}
	return 0.50
}

// filterAlertable drops disqualified and weak issues before anything is sent.
// Scoring on its own is not a filter: the scorer can return -1 and the alert
// still goes out unless someone acts on it, which is what happened with
// kubernetes#141200 ("[Feature:GPUDevicePlugin]", score -1.00, alerted).
func filterAlertable(issues []Issue) []Issue {
	floor := minAlertScore()
	kept := make([]Issue, 0, len(issues))
	var disqualified, weak int
	for _, iss := range issues {
		switch {
		case iss.Score < 0:
			disqualified++
		case iss.Score < floor:
			weak++
		default:
			kept = append(kept, iss)
		}
	}
	if disqualified > 0 || weak > 0 {
		GetLogger().Info("Filtered %d disqualified + %d below %.2f; %d alertable",
			disqualified, weak, floor, len(kept))
	}

	// Cap the burst. Once the ML/AI repos stopped crowding the top-30 the scan
	// surfaced a real backlog and fired 49 Telegram messages in one go, which
	// is unusable. Send the best few and let the rest come round next cycle -
	// they stay in the DB and are not lost.
	sort.Slice(kept, func(i, j int) bool { return kept[i].Score > kept[j].Score })

	// Spread the burst across repos before capping it. A single busy repo can
	// otherwise own the whole budget: golang/go filed 11 of 28 alerts in one
	// night, so every other project was invisible that day even though the
	// scan had found them.
	if perRepo := maxAlertsPerRepo(); perRepo > 0 {
		seen := map[string]int{}
		spread := make([]Issue, 0, len(kept))
		var crowded int
		for _, iss := range kept {
			key := iss.Project.Org + "/" + iss.Project.Name
			if seen[key] >= perRepo {
				crowded++
				continue
			}
			seen[key]++
			spread = append(spread, iss)
		}
		if crowded > 0 {
			GetLogger().Info("Held back %d alerts over %d per repo (they stay in the DB for the next cycle)",
				crowded, perRepo)
		}
		kept = spread
	}

	if max := maxAlertsPerRun(); max > 0 && len(kept) > max {
		GetLogger().Info("Capping alerts: %d alertable, sending top %d by score", len(kept), max)
		kept = kept[:max]
	}
	return kept
}

// maxAlertsPerRepo bounds how many alerts one repo may contribute to a single
// cycle. Override with MAX_ALERTS_PER_REPO; 0 disables the spread.
func maxAlertsPerRepo() int {
	if raw := strings.TrimSpace(os.Getenv("MAX_ALERTS_PER_REPO")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			return v
		}
	}
	return 2
}

// maxAlertsPerRun bounds how many alerts a single cycle may send.
// Override with MAX_ALERTS_PER_RUN; 0 disables the cap.
func maxAlertsPerRun() int {
	if raw := strings.TrimSpace(os.Getenv("MAX_ALERTS_PER_RUN")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			return v
		}
	}
	return 6
}

// dropTakenIssues removes issues that already have an open or recently
// stalled linked PR. Failing open is deliberate: a flaky timeline call should
// surface a maybe-taken issue rather than silently swallow a good one.
func (f *IssueFinder) dropTakenIssues(ctx context.Context, issues []Issue) []Issue {
	kept := make([]Issue, 0, len(issues))
	for _, iss := range issues {
		taken, reason := collectLinkedPRs(ctx, iss.Project, iss.Number,
			func(cb func() (*github.Response, error)) error {
				return f.rateLimiter.executeWithRetry(ctx,
					fmt.Sprintf("timeline %s/%s#%d", iss.Project.Org, iss.Project.Name, iss.Number), cb)
			},
			f.client,
			func(n int) *github.PullRequest {
				pr, _, err := f.client.PullRequests.Get(ctx, iss.Project.Org, iss.Project.Name, n)
				if err != nil {
					return nil
				}
				return pr
			},
		)
		if taken {
			GetLogger().Info("Skipping %s/%s#%d - %s",
				iss.Project.Org, iss.Project.Name, iss.Number, reason)
			continue
		}
		kept = append(kept, iss)
	}
	return kept
}

// botAuthors are accounts that file tracking issues rather than real work
// requests. GitHub reports gopherbot and bazel-io with type "User", so the
// account type alone is not enough to recognise them.
var botAuthors = map[string]bool{
	"gopherbot": true, "bazel-io": true, "k8s-ci-robot": true,
	"k8s-triage-robot": true, "openshift-bot": true, "cilium-renovate": true,
	"prow": true, "tide": true,
}

// isTrackingIssue reports whether an issue is project bookkeeping rather than
// something an outside contributor can pick up: flake collectors, backport
// requests, release forks and embargoed security trackers.
func isTrackingIssue(issue *github.Issue) bool {
	login := strings.ToLower(issue.GetUser().GetLogin())
	if botAuthors[login] || strings.HasSuffix(login, "[bot]") || issue.GetUser().GetType() == "Bot" {
		return true
	}
	if hasAnyLabel(issue.Labels, "automation", "cherrypickapproved",
		"cherrypickcandidate", "backport", "kind/backport") {
		return true
	}
	body := strings.ToLower(safeString(issue.Body))
	// Go's security process files a stub for an already-embargoed CVE; the
	// fix is written inside Google and only lands through the release team.
	if strings.Contains(body, "this is a private issue for cve") {
		return true
	}
	// bazel-io release forks, in case the account is ever renamed.
	return strings.HasPrefix(strings.TrimSpace(body), "forked from #")
}

// wordBoundaryCache keeps one compiled regexp per token so the hot scoring
// path does not recompile on every issue.
var (
	wordBoundaryMu    sync.Mutex
	wordBoundaryCache = map[string]*regexp.Regexp{}
)

// nonLatinScripts are the writing systems that put an issue out of reach here.
// Han also covers the kanji in Japanese text, which is the intent - the gate is
// about whether the report can be read, not about which language it is.
var nonLatinScripts = []*unicode.RangeTable{
	unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul,
	unicode.Cyrillic, unicode.Arabic, unicode.Hebrew, unicode.Thai,
}

// mostlyNonLatin reports whether enough of the text is written in a non-Latin
// script that the issue cannot be worked on.
//
// It is a ratio rather than a plain "contains" check on purpose. Plenty of
// perfectly good English issues quote a stack trace or a log line with a few
// CJK characters in it, and those must survive; an issue actually written in
// Chinese is dominated by them. The floor of 8 characters keeps a stray glyph
// in an otherwise English title from tripping the gate.
func mostlyNonLatin(text string) bool {
	var latin, other int
	for _, r := range text {
		switch {
		case unicode.IsOneOf(nonLatinScripts, r):
			other++
		case unicode.IsLetter(r):
			latin++
		}
	}
	if other < 8 {
		return false
	}
	// CJK is dense: a title carrying real content in it is short in character
	// count next to the equivalent English, so the bar sits low deliberately.
	return other*5 >= latin
}

// windowsWordRe finds the word "windows" together with whatever word precedes
// it, so the two meanings can be told apart.
var windowsWordRe = regexp.MustCompile(`(?:(\w+)[\s-]+)?\bwindows\b`)

// windowsTimeSenses are the words that make "windows" a time range rather than
// the operating system. Monitoring projects are full of them: Prometheus talks
// about staleness windows, Thanos about compaction windows, KEDA about scaling
// windows. Matching bare "windows" without this check would quietly disqualify
// most of the observability backlog.
var windowsTimeSenses = map[string]bool{
	"staleness": true, "sliding": true, "rolling": true, "time": true,
	"lookback": true, "retention": true, "scrape": true, "evaluation": true,
	"aggregation": true, "sampling": true, "observation": true, "grace": true,
	"maintenance": true, "compaction": true, "scaling": true, "cooldown": true,
	"tumbling": true, "overlapping": true, "congestion": true, "receive": true,
	"tcp": true, "these": true, "those": true, "such": true,
}

// mentionsWindowsOS reports whether "windows" appears in the operating-system
// sense anywhere in the text. One OS-sense hit is enough: ollama#17591
// ("Windows: ollama create fails with 400 Bad Request") was alerted at score
// 1.03 because the OS name only appeared as a bare title prefix, which none of
// the phrase patterns covered.
func mentionsWindowsOS(text string) bool {
	for _, m := range windowsWordRe.FindAllStringSubmatch(text, -1) {
		if !windowsTimeSenses[m[1]] {
			return true
		}
	}
	return false
}

// containsAnyWord reports whether text contains any keyword as a whole word.
// Needed for short ambiguous tokens: as a bare substring "aks" matches
// "breaks", "eks" matches "weeks" and "rds" matches "words".
func containsAnyWord(text string, keywords []string) bool {
	return slices.ContainsFunc(keywords, func(kw string) bool {
		wordBoundaryMu.Lock()
		re, ok := wordBoundaryCache[kw]
		if !ok {
			re = regexp.MustCompile(`\b` + regexp.QuoteMeta(kw) + `\b`)
			wordBoundaryCache[kw] = re
		}
		wordBoundaryMu.Unlock()
		return re.MatchString(text)
	})
}

func safeString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *IssueScorer) normalizeStars(stars int) float64 {
	if stars <= 1000 {
		return 0.3
	} else if stars <= 10000 {
		return 0.6
	} else if stars <= 50000 {
		return 0.8
	}
	return 1.0
}

func (s *IssueScorer) normalizeComments(comments int) float64 {
	if comments <= 2 {
		return 0.7
	} else if comments <= 5 {
		return 0.5
	} else if comments <= 10 {
		return 0.3
	}
	return 0.1
}

func (s *IssueScorer) normalizeRecency(createdAt time.Time) float64 {
	age := time.Since(createdAt).Hours()
	if age <= 24 {
		return 1.0
	} else if age <= 72 {
		return 0.8
	} else if age <= 168 {
		return 0.6
	} else if age <= 720 {
		return 0.4
	}
	return 0.2
}

func (s *IssueScorer) normalizeLabels(labels []*github.Label) float64 {
	score := 0.0
	hasGoodLabels := false
	hasBadLabels := false

	for _, label := range labels {
		labelName := strings.ToLower(label.GetName())

		if strings.Contains(labelName, "good first issue") ||
			strings.Contains(labelName, "help wanted") ||
			strings.Contains(labelName, "bug") ||
			strings.Contains(labelName, "enhancement") {
			score += 0.3
			hasGoodLabels = true
		}

		if strings.Contains(labelName, "documentation") {
			score += 0.2
			hasGoodLabels = true
		}

		if strings.Contains(labelName, "complex") ||
			strings.Contains(labelName, "hard") ||
			strings.Contains(labelName, "refactor") {
			hasBadLabels = true
		}
	}

	if hasGoodLabels && !hasBadLabels {
		return 1.0
	}
	return score
}

func (s *IssueScorer) normalizeDifficulty(labels []*github.Label, body string) float64 {
	hasGoodFirst := false
	bodyLower := strings.ToLower(body)

	for _, label := range labels {
		labelName := strings.ToLower(label.GetName())
		if strings.Contains(labelName, "good first issue") {
			hasGoodFirst = true
			break
		}
	}

	if hasGoodFirst {
		return 0.7
	}

	if strings.Contains(bodyLower, "simple") ||
		strings.Contains(bodyLower, "basic") ||
		strings.Contains(bodyLower, "small") {
		return 0.6
	}

	if strings.Contains(bodyLower, "complex") ||
		strings.Contains(bodyLower, "difficult") ||
		strings.Contains(bodyLower, "challenging") {
		return 0.2
	}

	return 0.4
}

type IssueFinder struct {
	config        *Config
	client        *github.Client
	rateLimiter   *RateLimiter
	bot           *tgbotapi.BotAPI
	notifier      *LocalNotifier
	db            *sqlx.DB
	scorer        *IssueScorer
	projects      []Project
	seenIssues    map[string]bool
	tracker       *IssueTracker
	assignmentMgr *AssignmentManager
	antiSpam      *NotificationSpamManager
	autoFinder    *AutoFinder
	repoManager   *RepoManager
	fileStore     *FileStorage
	monitor       *IssueMonitor
	mu            sync.RWMutex
	// convention-aware Telegram actions
	contribPolicies map[string]ContributionConvention
	contribPolicy   *ContributionPolicy
}

func NewIssueFinder(config *Config, notifier *LocalNotifier) (*IssueFinder, error) {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: config.GitHubToken},
	)
	tc := oauth2.NewClient(ctx, ts)

	var bot *tgbotapi.BotAPI
	if config.TelegramBotToken != "" {
		var err error
		bot, err = tgbotapi.NewBotAPI(config.TelegramBotToken)
		if err != nil {
			log.Printf("Warning: failed to create Telegram bot (Telegram disabled): %v", err)
			bot = nil
		}
	}

	GetLogger().Info("=== main() starting ===")
	GetLogger().Info("DB connection string: %s", maskDBConn(config.DBConnectionString))
	db, err := sqlx.Connect("postgres", config.DBConnectionString)
	if err != nil {
		GetLogger().Warn("Failed to connect to database, retrying: %v", err)
		// Retry connection with backoff rather than crashing
		for retries := 0; retries < 5; retries++ {
			GetLogger().Warn("DB reconnect attempt %d/5...", retries+1)
			time.Sleep(time.Duration(retries+1) * time.Second)
			db, err = sqlx.Connect("postgres", config.DBConnectionString)
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("failed to connect to database after retries: %w", err)
		}
	}
	GetLogger().Info("Database connection established successfully")

	client := github.NewClient(tc)
	rateLimiter := NewRateLimiter(client, 100)

	GetLogger().Info("Initializing rate limiter with 100 request buffer...")
	if err := rateLimiter.checkRateLimit(ctx); err != nil {
		GetLogger().Warn("Failed to fetch initial rate limits: %v", err)
	}

	finder := &IssueFinder{
		config:      config,
		client:      client,
		rateLimiter: rateLimiter,
		bot:         bot,
		notifier:    notifier,
		db:          db,
		scorer:      NewIssueScorer(),
		seenIssues:  make(map[string]bool),
	}

	if err := finder.initDB(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	if err := finder.loadSeenIssues(); err != nil {
		log.Printf("Warning: failed to load seen issues: %v", err)
	}

	tracker, err := NewIssueTracker(db.DB)
	if err != nil {
		log.Printf("Warning: failed to create issue tracker: %v", err)
	} else {
		finder.tracker = tracker
	}

	antiSpamConfig := config.AntiSpam
	if antiSpamConfig == nil {
		antiSpamConfig = &NotificationSpamConfig{}
	}
	antiSpamManager, err := NewNotificationSpamManager(*antiSpamConfig, db.DB)
	if err != nil {
		log.Printf("Warning: failed to create anti-spam manager: %v", err)
	} else {
		finder.antiSpam = antiSpamManager
	}

	if config.Assignment != nil && config.Assignment.Enabled {
		username := ""
		user, _, err := client.Users.Get(ctx, "")
		if err == nil && user != nil {
			username = user.GetLogin()
		}
		assignmentMgr, err := NewAssignmentManager(client, db.DB, username, true, config.Assignment.AutoMode)
		if err != nil {
			log.Printf("Warning: failed to create assignment manager: %v", err)
		} else {
			finder.assignmentMgr = assignmentMgr
			log.Printf("Assignment manager enabled (auto: %v)", config.Assignment.AutoMode)
		}
	}

	finder.repoManager = NewRepoManager()

	fileStore, err := NewFileStorage("")
	if err != nil {
		log.Printf("Warning: failed to create file storage: %v", err)
	} else {
		finder.fileStore = fileStore
	}

	autoFinderConfig := LoadAutoFinderConfigFromEnv()
	autoFinder, err := NewAutoFinder(autoFinderConfig, db, client, antiSpamManager)
	if err != nil {
		log.Printf("Warning: failed to create auto finder: %v", err)
	} else {
		finder.autoFinder = autoFinder
		log.Printf("Auto finder initialized (enabled: %v)", autoFinderConfig.Enabled)
	}

	monitorConfig := DefaultMonitorConfig()
	monitor, err := NewIssueMonitor(monitorConfig, client, notifier, fileStore)
	if err != nil {
		log.Printf("Warning: failed to create issue monitor: %v", err)
	} else {
		finder.monitor = monitor
		log.Printf("Issue monitor initialized (enabled: %v)", monitorConfig.Enabled)
	}

	finder.initializeProjects()

	// Convention-aware Telegram actions: load per-repo contribution policy and
	// the trust-based ContributionPolicy engine, then start the button poller.
	finder.contribPolicies = LoadConventions()
	if config.GitHubToken != "" {
		// Resolve the authenticated user's login for the ContributionPolicy engine.
		ghUser, _, uErr := client.Users.Get(ctx, "")
		ghLogin := ""
		if uErr == nil && ghUser != nil {
			ghLogin = ghUser.GetLogin()
		}
		if cp := NewContributionPolicy(client, db, ghLogin); cp != nil {
			finder.contribPolicy = cp
		}
	}
	go finder.StartCallbackPoller()

	return finder, nil
}

func (f *IssueFinder) initDB() error {
	schema := `
	CREATE TABLE IF NOT EXISTS seen_issues (
		id SERIAL PRIMARY KEY,
		issue_id TEXT UNIQUE NOT NULL,
		project_name TEXT NOT NULL,
		first_seen TIMESTAMP NOT NULL,
		last_notified TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS issue_history (
		id SERIAL PRIMARY KEY,
		issue_id TEXT NOT NULL,
		issue_title TEXT NOT NULL,
		issue_url TEXT NOT NULL,
		project_name TEXT NOT NULL,
		category TEXT NOT NULL,
		score FLOAT NOT NULL,
		comments INTEGER NOT NULL,
		labels TEXT,
		created_at TIMESTAMP NOT NULL,
		discovered_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_seen_issues_issue_id ON seen_issues(issue_id);
	CREATE INDEX IF NOT EXISTS idx_issue_history_issue_id ON issue_history(issue_id);
	CREATE INDEX IF NOT EXISTS idx_issue_history_score ON issue_history(score DESC);
	CREATE INDEX IF NOT EXISTS idx_issue_history_created_at ON issue_history(created_at DESC);
	`

	_, err := f.db.Exec(schema)
	return err
}

func (f *IssueFinder) loadSeenIssues() error {
	var issueIDs []string
	err := f.db.Select(&issueIDs, "SELECT issue_id FROM seen_issues")
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, id := range issueIDs {
		f.seenIssues[id] = true
	}

	return nil
}

func (f *IssueFinder) markIssueSeen(issueID, projectName string) error {
	f.mu.Lock()
	f.seenIssues[issueID] = true
	f.mu.Unlock()

	_, err := f.db.Exec(`
		INSERT INTO seen_issues (issue_id, project_name, first_seen, last_notified)
		VALUES ($1, $2, $3, $3)
		ON CONFLICT (issue_id) DO UPDATE SET last_notified = $3
	`, issueID, projectName, time.Now())

	return err
}

func (f *IssueFinder) saveIssueHistory(issue Issue) error {
	labelsJSON, _ := json.Marshal(issue.Labels)

	_, err := f.db.Exec(`
		INSERT INTO issue_history (issue_id, issue_title, issue_url, project_name, category, score, comments, labels, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING
	`, fmt.Sprintf("%s/%d", issue.Project.Name, issue.Number), issue.Title, issue.URL, issue.Project.Name, issue.Project.Category, issue.Score, issue.Comments, labelsJSON, issue.CreatedAt)

	return err
}

func (f *IssueFinder) initializeProjects() {
	f.projects = []Project{
		{Org: "kubernetes", Name: "kubernetes", Category: "Kubernetes", Stars: 105000},
		{Org: "prometheus", Name: "prometheus", Category: "Monitoring", Stars: 53000},
		{Org: "argoproj", Name: "argo-cd", Category: "CI/CD", Stars: 15000},
		{Org: "thanos-io", Name: "thanos", Category: "Monitoring", Stars: 12000},
		{Org: "fluxcd", Name: "flux2", Category: "CI/CD", Stars: 6000},
		{Org: "jaegertracing", Name: "jaeger", Category: "Monitoring", Stars: 19000},
		{Org: "open-telemetry", Name: "opentelemetry-collector", Category: "Monitoring", Stars: 3500},
		{Org: "helm", Name: "helm", Category: "Kubernetes", Stars: 25000},
		{Org: "knative", Name: "knative", Category: "Kubernetes", Stars: 6000},
		{Org: "vmware-tanzu", Name: "velero", Category: "Kubernetes", Stars: 8000},
		{Org: "tektoncd", Name: "pipeline", Category: "CI/CD", Stars: 8000},
		{Org: "argoproj", Name: "argo-events", Category: "CI/CD", Stars: 2000},
		{Org: "argoproj", Name: "argo-rollouts", Category: "CI/CD", Stars: 2000},
		{Org: "cilium", Name: "cilium", Category: "Kubernetes", Stars: 18000},
		{Org: "linkerd", Name: "linkerd2", Category: "Kubernetes", Stars: 10000},
		{Org: "hashicorp", Name: "consul", Category: "Kubernetes", Stars: 27000},
		{Org: "hashicorp", Name: "vault", Category: "Kubernetes", Stars: 29000},
		{Org: "hashicorp", Name: "nomad", Category: "Kubernetes", Stars: 14000},
		{Org: "coredns", Name: "coredns", Category: "Kubernetes", Stars: 11000},
		{Org: "containerd", Name: "containerd", Category: "Kubernetes", Stars: 15000},
		{Org: "rook", Name: "rook", Category: "Kubernetes", Stars: 12000},
		{Org: "longhorn", Name: "longhorn", Category: "Kubernetes", Stars: 5000},
		{Org: "kedacore", Name: "keda", Category: "Kubernetes", Stars: 7500},
		{Org: "grafana", Name: "grafana", Category: "Monitoring", Stars: 58000},
		{Org: "grafana", Name: "loki", Category: "Monitoring", Stars: 21000},
		{Org: "grafana", Name: "tempo", Category: "Monitoring", Stars: 5500},
		{Org: "grafana", Name: "mimir", Category: "Monitoring", Stars: 4000},
		{Org: "prometheus-operator", Name: "prometheus-operator", Category: "Monitoring", Stars: 8500},
		{Org: "prometheus", Name: "alertmanager", Category: "Monitoring", Stars: 6500},
		{Org: "fluxcd", Name: "flagger", Category: "Kubernetes", Stars: 4500},
		{Org: "grafana", Name: "promtail", Category: "Monitoring", Stars: 1500},
		{Org: "prometheus", Name: "pushgateway", Category: "Monitoring", Stars: 2500},
		{Org: "VictoriaMetrics", Name: "VictoriaMetrics", Category: "Monitoring", Stars: 10000},
		{Org: "telepresenceio", Name: "telepresence", Category: "Kubernetes", Stars: 3500},
		{Org: "crossplane", Name: "crossplane", Category: "Kubernetes", Stars: 6500},
		{Org: "k3s-io", Name: "k3s", Category: "Kubernetes", Stars: 25000},
		{Org: "rancher", Name: "rke2", Category: "Kubernetes", Stars: 4000},
		{Org: "k0sproject", Name: "k0s", Category: "Kubernetes", Stars: 4000},
		{Org: "kubewarden", Name: "kubewarden-controller", Category: "Kubernetes", Stars: 800},
		{Org: "open-policy-agent", Name: "opa", Category: "Kubernetes", Stars: 9000},
		{Org: "kyverno", Name: "kyverno", Category: "Kubernetes", Stars: 5000},
		{Org: "falcosecurity", Name: "falco", Category: "Kubernetes", Stars: 5000},
		{Org: "aquasecurity", Name: "trivy", Category: "Kubernetes", Stars: 21000},
		{Org: "anchore", Name: "grype", Category: "Kubernetes", Stars: 6000},
		{Org: "vmware-tanzu", Name: "sonobuoy", Category: "Kubernetes", Stars: 3000},
		{Org: "vmware-tanzu", Name: "octant", Category: "Kubernetes", Stars: 7000},
		{Org: "derailed", Name: "k9s", Category: "Kubernetes", Stars: 24000},
		{Org: "ahmetb", Name: "kubectx", Category: "Kubernetes", Stars: 16000},
		{Org: "johanhaleby", Name: "kubetail", Category: "Kubernetes", Stars: 2500},
		{Org: "kubernetes-sigs", Name: "krew", Category: "Kubernetes", Stars: 5500},
		{Org: "kubernetes-sigs", Name: "kubectl-plugins", Category: "Kubernetes", Stars: 1200},
		{Org: "zegl", Name: "kube-score", Category: "Kubernetes", Stars: 3000},
		{Org: "aquasecurity", Name: "kube-bench", Category: "Kubernetes", Stars: 6000},
		{Org: "FairwindsOps", Name: "polaris", Category: "Kubernetes", Stars: 2500},
		{Org: "FairwindsOps", Name: "goldilocks", Category: "Kubernetes", Stars: 2000},
		{Org: "stackrox", Name: "kube-linter", Category: "Kubernetes", Stars: 2000},
		{Org: "FairwindsOps", Name: "pluto", Category: "Kubernetes", Stars: 1200},
		{Org: "FairwindsOps", Name: "nozzle", Category: "Kubernetes", Stars: 400},
		{Org: "FairwindsOps", Name: "rbac-lookup", Category: "Kubernetes", Stars: 900},
		{Org: "FairwindsOps", Name: "rbac-manager", Category: "Kubernetes", Stars: 900},
		{Org: "schemahero", Name: "schemahero", Category: "Kubernetes", Stars: 1100},
		{Org: "loft-sh", Name: "vcluster", Category: "Kubernetes", Stars: 4500},
		{Org: "clastix", Name: "kamaji", Category: "Kubernetes", Stars: 500},
		{Org: "karmada-io", Name: "karmada", Category: "Kubernetes", Stars: 3500},
		{Org: "liqotech", Name: "liqo", Category: "Kubernetes", Stars: 800},
		{Org: "volcano-sh", Name: "volcano", Category: "Kubernetes", Stars: 3000},
		{Org: "openkruise", Name: "kruise", Category: "Kubernetes", Stars: 4500},
		{Org: "kubevela", Name: "kubevela", Category: "Kubernetes", Stars: 5500},
		{Org: "crossplane", Name: "oam-kubernetes-runtime", Category: "Kubernetes", Stars: 1000},
		{Org: "kubernetes", Name: "dashboard", Category: "Kubernetes", Stars: 13000},
		{Org: "kubernetes", Name: "kube-state-metrics", Category: "Monitoring", Stars: 5000},
		{Org: "prometheus", Name: "node_exporter", Category: "Monitoring", Stars: 10000},
		{Org: "google", Name: "cadvisor", Category: "Monitoring", Stars: 16000},
		{Org: "prometheus", Name: "blackbox_exporter", Category: "Monitoring", Stars: 4000},
		{Org: "justwatchcom", Name: "sql_exporter", Category: "Monitoring", Stars: 400},
		{Org: "prometheus", Name: "jmx_exporter", Category: "Monitoring", Stars: 3000},
		{Org: "prometheus", Name: "statsd_exporter", Category: "Monitoring", Stars: 1200},
		{Org: "smartping", Name: "smartping_exporter", Category: "Monitoring", Stars: 200},
		{Org: "czerwonk", Name: "ping_exporter", Category: "Monitoring", Stars: 250},
		{Org: "cassandra", Name: "cassandra_exporter", Category: "Monitoring", Stars: 300},
		{Org: "percona", Name: "mongodb_exporter", Category: "Monitoring", Stars: 600},
		{Org: "oliver006", Name: "redis_exporter", Category: "Monitoring", Stars: 4000},
		{Org: "prometheus-community", Name: "postgres_exporter", Category: "Monitoring", Stars: 2000},
		{Org: "prometheus", Name: "mysqld_exporter", Category: "Monitoring", Stars: 2000},
		{Org: "nginxinc", Name: "nginx-prometheus-exporter", Category: "Monitoring", Stars: 1500},
		{Org: "prometheus", Name: "haproxy_exporter", Category: "Monitoring", Stars: 1000},

		{Org: "prometheus", Name: "consul_exporter", Category: "Monitoring", Stars: 400},
		{Org: "prometheus", Name: "etcd_exporter", Category: "Monitoring", Stars: 200},
		{Org: "dabealu", Name: "zookeeper_exporter", Category: "Monitoring", Stars: 200},
		{Org: "danielqsj", Name: "kafka_exporter", Category: "Monitoring", Stars: 2000},
		{Org: "kbudde", Name: "rabbitmq_exporter", Category: "Monitoring", Stars: 500},
		{Org: "xiaorui", Name: "thrift_exporter", Category: "Monitoring", Stars: 50},
		{Org: "trustpath", Name: "smtp_exporter", Category: "Monitoring", Stars: 50},
		{Org: "prometheus", Name: "http_exporter", Category: "Monitoring", Stars: 200},
		{Org: "oliver006", Name: "dns_exporter", Category: "Monitoring", Stars: 200},
		{Org: "tm", Name: "imap_exporter", Category: "Monitoring", Stars: 50},
		{Org: "tm", Name: "pop3_exporter", Category: "Monitoring", Stars: 50},
		{Org: "tm", Name: "ftp_exporter", Category: "Monitoring", Stars: 50},
		{Org: "prometheus-community", Name: "ssh_exporter", Category: "Monitoring", Stars: 100},
		{Org: "prometheus", Name: "collectd_exporter", Category: "Monitoring", Stars: 400},
		{Org: "prometheus", Name: "ganglia_exporter", Category: "Monitoring", Stars: 200},
		{Org: "prometheus", Name: "influxdb_exporter", Category: "Monitoring", Stars: 200},
		{Org: "prometheus", Name: "libvirt_exporter", Category: "Monitoring", Stars: 300},
		{Org: "prometheus", Name: "mesos_exporter", Category: "Monitoring", Stars: 100},
		{Org: "prometheus-community", Name: "puppetdb_exporter", Category: "Monitoring", Stars: 200},
		{Org: "prometheus", Name: "riak_exporter", Category: "Monitoring", Stars: 100},
		{Org: "prometheus", Name: "sensu_exporter", Category: "Monitoring", Stars: 100},
		{Org: "simon", Name: "puppet_exporter", Category: "Monitoring", Stars: 100},
		{Org: "prometheus", Name: "vault_exporter", Category: "Monitoring", Stars: 400},
		{Org: "prometheus-community", Name: "fluentd_exporter", Category: "Monitoring", Stars: 300},
		{Org: "prometheus-community", Name: "logstash_exporter", Category: "Monitoring", Stars: 200},
		{Org: "prometheus-community", Name: "beats_exporter", Category: "Monitoring", Stars: 200},
		{Org: "prometheus-community", Name: "prometheus-lens", Category: "Monitoring", Stars: 100},
		{Org: "thanos-io", Name: "thanos-receive-controller", Category: "Monitoring", Stars: 50},
		{Org: "thanos-io", Name: "thanos-store", Category: "Monitoring", Stars: 50},
		{Org: "thanos-io", Name: "thanos-query", Category: "Monitoring", Stars: 50},
		{Org: "thanos-io", Name: "thanos-compact", Category: "Monitoring", Stars: 50},
		{Org: "thanos-io", Name: "thanos-rule", Category: "Monitoring", Stars: 50},
		{Org: "thanos-io", Name: "thanos-sidecar", Category: "Monitoring", Stars: 50},
		{Org: "thanos-io", Name: "thanos-bucket", Category: "Monitoring", Stars: 50},
		{Org: "thanos-io", Name: "thanos-objstore", Category: "Monitoring", Stars: 50},
		{Org: "cortexproject", Name: "cortex", Category: "Monitoring", Stars: 5000},
		{Org: "grafana", Name: "agent", Category: "Monitoring", Stars: 1500},
		{Org: "grafana", Name: "oncall", Category: "Monitoring", Stars: 4000},
		{Org: "grafana", Name: "phlare", Category: "Monitoring", Stars: 3000},
		{Org: "grafana", Name: "synthetic-monitoring-agent", Category: "Monitoring", Stars: 200},
		{Org: "grafana", Name: "k6", Category: "Monitoring", Stars: 21000},
		{Org: "grafana", Name: "faraday", Category: "Monitoring", Stars: 500},
		{Org: "jaegertracing", Name: "jaeger-query", Category: "Monitoring", Stars: 50},
		{Org: "jaegertracing", Name: "jaeger-collector", Category: "Monitoring", Stars: 50},
		{Org: "jaegertracing", Name: "jaeger-agent", Category: "Monitoring", Stars: 50},
		{Org: "jaegertracing", Name: "jaeger-ingester", Category: "Monitoring", Stars: 50},
		{Org: "jaegertracing", Name: "jaeger-all-in-one", Category: "Monitoring", Stars: 50},
		{Org: "openzipkin", Name: "zipkin", Category: "Monitoring", Stars: 2000},
		{Org: "openzipkin", Name: "zipkin-ui", Category: "Monitoring", Stars: 50},
		{Org: "openzipkin", Name: "zipkin-collector", Category: "Monitoring", Stars: 50},
		{Org: "openzipkin", Name: "zipkin-query", Category: "Monitoring", Stars: 50},
		{Org: "openzipkin", Name: "zipkin-reporter", Category: "Monitoring", Stars: 50},
		{Org: "openzipkin", Name: "zipkin-storage", Category: "Monitoring", Stars: 50},
		{Org: "openzipkin", Name: "zipkin-dependencies", Category: "Monitoring", Stars: 50},
		{Org: "apache", Name: "skywalking", Category: "Monitoring", Stars: 22000},
		{Org: "open-telemetry", Name: "opentelemetry-collector-contrib", Category: "Monitoring", Stars: 2000},
		{Org: "open-telemetry", Name: "opentelemetry-go", Category: "Monitoring", Stars: 4500},
		{Org: "open-telemetry", Name: "opentelemetry-java", Category: "Monitoring", Stars: 3000},
		{Org: "open-telemetry", Name: "opentelemetry-python", Category: "Monitoring", Stars: 2000},
		{Org: "open-telemetry", Name: "opentelemetry-js", Category: "Monitoring", Stars: 1500},
		{Org: "open-telemetry", Name: "opentelemetry-cpp", Category: "Monitoring", Stars: 800},
		{Org: "open-telemetry", Name: "opentelemetry-rust", Category: "Monitoring", Stars: 1200},
		{Org: "open-telemetry", Name: "opentelemetry-dotnet", Category: "Monitoring", Stars: 1000},
		{Org: "open-telemetry", Name: "opentelemetry-php", Category: "Monitoring", Stars: 500},
		{Org: "open-telemetry", Name: "opentelemetry-ruby", Category: "Monitoring", Stars: 300},
		{Org: "open-telemetry", Name: "opentelemetry-erlang", Category: "Monitoring", Stars: 100},
		{Org: "open-telemetry", Name: "opentelemetry-swift", Category: "Monitoring", Stars: 200},
		{Org: "open-telemetry", Name: "opentelemetry-kotlin", Category: "Monitoring", Stars: 100},
		{Org: "open-telemetry", Name: "opentelemetry-scala", Category: "Monitoring", Stars: 100},
		{Org: "argoproj", Name: "argo-cd", Category: "CI/CD", Stars: 15000},
		{Org: "argoproj", Name: "argo-workflows", Category: "CI/CD", Stars: 14000},
		{Org: "argoproj", Name: "argo-events", Category: "CI/CD", Stars: 2000},
		{Org: "argoproj", Name: "argo-rollouts", Category: "CI/CD", Stars: 2000},
		{Org: "argoproj", Name: "argocd-image-updater", Category: "CI/CD", Stars: 900},
		{Org: "fluxcd", Name: "flux2", Category: "CI/CD", Stars: 6000},
		{Org: "fluxcd", Name: "helm-operator", Category: "CI/CD", Stars: 1000},
		{Org: "fluxcd", Name: "flux-operator", Category: "CI/CD", Stars: 500},
		{Org: "fluxcd", Name: "flagger", Category: "CI/CD", Stars: 4500},
		{Org: "tektoncd", Name: "pipeline", Category: "CI/CD", Stars: 8000},
		{Org: "tektoncd", Name: "triggers", Category: "CI/CD", Stars: 1000},
		{Org: "tektoncd", Name: "cli", Category: "CI/CD", Stars: 600},
		{Org: "tektoncd", Name: "dashboard", Category: "CI/CD", Stars: 400},
		{Org: "tektoncd", Name: "catalog", Category: "CI/CD", Stars: 300},
		{Org: "tektoncd", Name: "operator", Category: "CI/CD", Stars: 300},
		{Org: "tektoncd", Name: "results", Category: "CI/CD", Stars: 200},
		{Org: "tektoncd", Name: "chains", Category: "CI/CD", Stars: 200},
		{Org: "tektoncd", Name: "hub", Category: "CI/CD", Stars: 200},
		{Org: "kubernetes-sigs", Name: "prow", Category: "CI/CD", Stars: 4000},
		{Org: "argoproj", Name: "dispatch", Category: "CI/CD", Stars: 500},
		{Org: "keel-hq", Name: "keel", Category: "CI/CD", Stars: 2500},
		{Org: "containrrr", Name: "watchtower", Category: "CI/CD", Stars: 17000},
		{Org: "drone", Name: "drone", Category: "CI/CD", Stars: 28000},
		{Org: "gitlab", Name: "gitlab-runner", Category: "CI/CD", Stars: 4500},
		{Org: "woodpecker-ci", Name: "woodpecker", Category: "CI/CD", Stars: 4500},
		{Org: "gocd", Name: "gocd", Category: "CI/CD", Stars: 7000},
		{Org: "concourse", Name: "concourse", Category: "CI/CD", Stars: 7500},
		{Org: "screwdriver-cd", Name: "screwdriver", Category: "CI/CD", Stars: 1200},
		{Org: "jenkins-x", Name: "jenkins-x", Category: "CI/CD", Stars: 500},
		{Org: "jenkins-x", Name: "lighthouse", Category: "CI/CD", Stars: 200},
		{Org: "tektoncd", Name: "tekton", Category: "CI/CD", Stars: 300},
		{Org: "GoogleContainerTools", Name: "skaffold", Category: "CI/CD", Stars: 15000},
		{Org: "tilt-dev", Name: "tilt", Category: "CI/CD", Stars: 9000},
		{Org: "paketo-buildpacks", Name: "kpack", Category: "CI/CD", Stars: 1500},
		{Org: "buildpacks", Name: "pack", Category: "CI/CD", Stars: 2000},
		{Org: "google", Name: "ko", Category: "CI/CD", Stars: 3500},
		{Org: "genuinetools", Name: "img", Category: "CI/CD", Stars: 3000},
		{Org: "containers", Name: "buildah", Category: "CI/CD", Stars: 7000},
		{Org: "containers", Name: "podman", Category: "CI/CD", Stars: 20000},
		{Org: "GoogleContainerTools", Name: "kaniko", Category: "CI/CD", Stars: 13000},
		{Org: "openshift", Name: "source-to-image", Category: "CI/CD", Stars: 2000},
		{Org: "GoogleContainerTools", Name: "jib", Category: "CI/CD", Stars: 13000},
		{Org: "bazelbuild", Name: "bazel", Category: "CI/CD", Stars: 21000},
		{Org: "bazel-contrib", Name: "bazelisk", Category: "CI/CD", Stars: 1000},
		{Org: "thought-machine", Name: "please", Category: "CI/CD", Stars: 1500},
		{Org: "facebook", Name: "buck", Category: "CI/CD", Stars: 8000},
		{Org: "golang", Name: "make", Category: "CI/CD", Stars: 100},
		{Org: "kitware", Name: "cmake", Category: "CI/CD", Stars: 5000},
		{Org: "mesonbuild", Name: "meson", Category: "CI/CD", Stars: 3000},
		{Org: "ninja-build", Name: "ninja", Category: "CI/CD", Stars: 3000},
		{Org: "rust-lang", Name: "cargo", Category: "CI/CD", Stars: 5000},

		// AI infrastructure written in Go. Deliberately a separate category
		// from ML/AI: the ML/AI entries below are Python research frameworks
		// that this track excludes, while these are gateways, routers and
		// Kubernetes inference operators - the same controller, proxy and
		// scheduling work as the rest of the list, only serving models.
		// They run and test on bare metal.
		{Org: "envoyproxy", Name: "ai-gateway", Category: "AI-Infra", Stars: 1900},
		{Org: "kserve", Name: "kserve", Category: "AI-Infra", Stars: 5700},
		{Org: "vllm-project", Name: "aibrix", Category: "AI-Infra", Stars: 4900},
		{Org: "kagent-dev", Name: "kagent", Category: "AI-Infra", Stars: 3400},
		{Org: "kubeai-project", Name: "kubeai", Category: "AI-Infra", Stars: 1200},
		{Org: "llm-d", Name: "llm-d-router", Category: "AI-Infra", Stars: 280},
		{Org: "maximhq", Name: "bifrost", Category: "AI-Infra", Stars: 7000},
		{Org: "ollama", Name: "ollama", Category: "AI-Infra", Stars: 177000},

		// ML/AI Projects
		{Org: "tensorflow", Name: "tensorflow", Category: "ML/AI", Stars: 185000},
		{Org: "pytorch", Name: "pytorch", Category: "ML/AI", Stars: 85000},
		{Org: "huggingface", Name: "transformers", Category: "ML/AI", Stars: 140000},
		{Org: "langchain-ai", Name: "langchain", Category: "ML/AI", Stars: 100000},
		{Org: "openai", Name: "openai-python", Category: "ML/AI", Stars: 25000},
		{Org: "scikit-learn", Name: "scikit-learn", Category: "ML/AI", Stars: 60000},
		{Org: "keras-team", Name: "keras", Category: "ML/AI", Stars: 62000},
		{Org: "onnx", Name: "onnx", Category: "ML/AI", Stars: 18000},
		{Org: "microsoft", Name: "DeepSpeed", Category: "ML/AI", Stars: 35000},
		{Org: "Lightning-AI", Name: "lightning", Category: "ML/AI", Stars: 28000},
		{Org: "explosion", Name: "spaCy", Category: "ML/AI", Stars: 30000},
		{Org: "stanfordnlp", Name: "CoreNLP", Category: "ML/AI", Stars: 9500},
		{Org: "paddlepaddle", Name: "paddle", Category: "ML/AI", Stars: 22000},
		{Org: "apache", Name: "mxnet", Category: "ML/AI", Stars: 21000},
		{Org: "Theano", Name: "Theano", Category: "ML/AI", Stars: 10000},
		{Org: "cupy", Name: "cupy", Category: "ML/AI", Stars: 8000},
		{Org: "dmlc", Name: "xgboost", Category: "ML/AI", Stars: 26000},
		{Org: "microsoft", Name: "LightGBM", Category: "ML/AI", Stars: 17000},
		{Org: "dmlc", Name: "tvm", Category: "ML/AI", Stars: 11000},
		{Org: "ray-project", Name: "ray", Category: "ML/AI", Stars: 35000},
		{Org: "fastai", Name: "fastai", Category: "ML/AI", Stars: 26000},
		{Org: "Stability-AI", Name: "stablediffusion", Category: "ML/AI", Stars: 40000},
		{Org: "CompVis", Name: "stable-diffusion", Category: "ML/AI", Stars: 70000},
		{Org: "AUTOMATIC1111", Name: "stable-diffusion-webui", Category: "ML/AI", Stars: 145000},
		{Org: "mlflow", Name: "mlflow", Category: "ML/AI", Stars: 19000},
		{Org: "wandb", Name: "wandb", Category: "ML/AI", Stars: 9000},
		{Org: "apache", Name: "airflow", Category: "ML/AI", Stars: 38000},
		{Org: "prefecthq", Name: "prefect", Category: "ML/AI", Stars: 16000},
		{Org: "pinecone-io", Name: "pinecone-python-client", Category: "ML/AI", Stars: 3000},
		{Org: "weaviate", Name: "weaviate", Category: "ML/AI", Stars: 12000},
		{Org: "milvus-io", Name: "milvus", Category: "ML/AI", Stars: 31000},
		{Org: "qdrant", Name: "qdrant", Category: "ML/AI", Stars: 21000},
		{Org: "chroma-core", Name: "chroma", Category: "ML/AI", Stars: 15000},
		{Org: "llama-index", Name: "llama_index", Category: "ML/AI", Stars: 38000},
		{Org: "deepset-ai", Name: "haystack", Category: "ML/AI", Stars: 18000},
		{Org: "obhava", Name: "obhava", Category: "ML/AI", Stars: 1000},
		{Org: "vllm-project", Name: "vllm", Category: "ML/AI", Stars: 30000},
		{Org: "ggerganov", Name: "llama.cpp", Category: "ML/AI", Stars: 70000},
		{Org: "lm-sys", Name: "FastChat", Category: "ML/AI", Stars: 37000},
		{Org: "oobabooga", Name: "text-generation-webui", Category: "ML/AI", Stars: 42000},
		{Org: "microsoft", Name: "semantic-kernel", Category: "ML/AI", Stars: 22000},
		{Org: "microsoft", Name: "autogen", Category: "ML/AI", Stars: 32000},
		{Org: "langchain-ai", Name: "langgraph", Category: "ML/AI", Stars: 10000},
		{Org: "run-llama", Name: "llama_index", Category: "ML/AI", Stars: 38000},
		{Org: "unslothai", Name: "unsloth", Category: "ML/AI", Stars: 10000},
		{Org: "axolotl-ai-cloud", Name: "axolotl", Category: "ML/AI", Stars: 8000},

		// Additional Go Projects - CNCF, Security, Networking, TLS
		{Org: "istio", Name: "istio", Category: "TLS/Security", Stars: 35000},
		{Org: "traefik", Name: "traefik", Category: "TLS/Security", Stars: 50000},
		{Org: "caddyserver", Name: "caddy", Category: "TLS/Security", Stars: 58000},
		{Org: "grpc", Name: "grpc-go", Category: "TLS/Security", Stars: 21000},
		{Org: "dapr", Name: "dapr", Category: "TLS/Security", Stars: 24000},
		{Org: "kubernetes", Name: "ingress-nginx", Category: "TLS/Security", Stars: 17000},
		{Org: "oauth2-proxy", Name: "oauth2-proxy", Category: "TLS/Security", Stars: 9000},
		{Org: "cert-manager", Name: "cert-manager", Category: "TLS/Security", Stars: 12000},
		{Org: "external-secrets", Name: "external-secrets", Category: "TLS/Security", Stars: 4000},
		{Org: "secrets-store-csi-driver", Name: "secrets-store-csi-driver", Category: "TLS/Security", Stars: 1500},
		{Org: "spiffe", Name: "spire", Category: "TLS/Security", Stars: 2000},
		{Org: "open-policy-agent", Name: "gatekeeper", Category: "TLS/Security", Stars: 3500},
		{Org: "cloudflare", Name: "cfssl", Category: "TLS/Security", Stars: 2000},
		{Org: "smallstep", Name: "certificates", Category: "TLS/Security", Stars: 6000},
		{Org: "jetstack", Name: "cert-manager", Category: "TLS/Security", Stars: 12000},
		{Org: "hashicorp", Name: "boundary", Category: "TLS/Security", Stars: 5000},
		{Org: "hashicorp", Name: "waypoint", Category: "TLS/Security", Stars: 5000},
		{Org: "sosedoff", Name: "pgweb", Category: "TLS/Security", Stars: 9000},
		{Org: "gorush", Name: "gorush", Category: "Go Tools", Stars: 8000},
		{Org: "goreleaser", Name: "goreleaser", Category: "Go Tools", Stars: 14000},
		{Org: "golangci", Name: "golangci-lint", Category: "Go Tools", Stars: 15000},
		{Org: "stretchr", Name: "testify", Category: "Go Tools", Stars: 23000},
		{Org: "uber-go", Name: "zap", Category: "Go Tools", Stars: 22000},
		{Org: "uber-go", Name: "fx", Category: "Go Tools", Stars: 6000},
		{Org: "uber-go", Name: "dig", Category: "Go Tools", Stars: 4000},
		{Org: "spf13", Name: "cobra", Category: "Go Tools", Stars: 38000},
		{Org: "spf13", Name: "viper", Category: "Go Tools", Stars: 27000},
		{Org: "urfave", Name: "cli", Category: "Go Tools", Stars: 22000},
		{Org: "joho", Name: "godotenv", Category: "Go Tools", Stars: 8000},
		{Org: "go-playground", Name: "validator", Category: "Go Tools", Stars: 17000},
		{Org: "swaggo", Name: "swag", Category: "Go Tools", Stars: 110000},
		{Org: "golang-migrate", Name: "migrate", Category: "Go Tools", Stars: 150000},
		{Org: "ent", Name: "ent", Category: "Go Tools", Stars: 15000},
		{Org: "go-gorm", Name: "gorm", Category: "Go Tools", Stars: 37000},
		{Org: "go-redis", Name: "redis", Category: "TLS/Security", Stars: 20000},
		{Org: "minio", Name: "minio", Category: "TLS/Security", Stars: 45000},
		{Org: "nutsdb", Name: "nutsdb", Category: "Go Tools", Stars: 3000},
		{Org: "tidwall", Name: "gjson", Category: "Go Tools", Stars: 14000},
		{Org: "tidwall", Name: "sjson", Category: "Go Tools", Stars: 2000},
		{Org: "tidwall", Name: "buntdb", Category: "Go Tools", Stars: 4000},
		{Org: "klauspost", Name: "compress", Category: "Go Tools", Stars: 5000},
		{Org: "valyala", Name: "fasthttp", Category: "Go Web", Stars: 22000},
		{Org: "panjf2000", Name: "ants", Category: "Go Tools", Stars: 13000},
		{Org: "shirou", Name: "gopsutil", Category: "Go Tools", Stars: 11000},
		{Org: "mitchellh", Name: "mapstructure", Category: "Go Tools", Stars: 8000},
		{Org: "google", Name: "wire", Category: "Go Tools", Stars: 13000},
		{Org: "google", Name: "go-cmp", Category: "Go Tools", Stars: 4000},
		{Org: "pkg", Name: "errors", Category: "Go Tools", Stars: 9000},
		{Org: "fsnotify", Name: "fsnotify", Category: "Go Tools", Stars: 10000},
		{Org: "asaskevich", Name: "govalidator", Category: "Go Tools", Stars: 6000},
		{Org: "go-ozzo", Name: "ozzo-validation", Category: "Go Tools", Stars: 4000},
		{Org: "gofrs", Name: "uuid", Category: "Go Tools", Stars: 2000},
		{Org: "google", Name: "uuid", Category: "Go Tools", Stars: 6000},
		{Org: "rs", Name: "zerolog", Category: "Go Tools", Stars: 11000},
		{Org: "sirupsen", Name: "logrus", Category: "Go Tools", Stars: 25000},
		{Org: "opentracing", Name: "opentracing-go", Category: "TLS/Security", Stars: 4000},
		{Org: "open-telemetry", Name: "opentelemetry-go", Category: "TLS/Security", Stars: 4500},
		{Org: "open-telemetry", Name: "opentelemetry-collector", Category: "TLS/Security", Stars: 3500},
		{Org: "cloudnative-pg", Name: "cloudnative-pg", Category: "Kubernetes", Stars: 5000},
		{Org: "operator-framework", Name: "operator-sdk", Category: "Kubernetes", Stars: 7000},
		{Org: "kubebuilder", Name: "kubebuilder", Category: "Kubernetes", Stars: 8000},
		{Org: "controller-runtime", Name: "controller-runtime", Category: "Kubernetes", Stars: 3000},
		{Org: "kubernetes-sigs", Name: "kind", Category: "Kubernetes", Stars: 14000},
		{Org: "kubernetes-sigs", Name: "kustomize", Category: "Kubernetes", Stars: 11000},
		{Org: "kubernetes-sigs", Name: "cluster-api", Category: "Kubernetes", Stars: 4000},
		{Org: "kubernetes-sigs", Name: "kubebuilder", Category: "Kubernetes", Stars: 8000},
		{Org: "gravitational", Name: "teleport", Category: "TLS/Security", Stars: 18000},
		{Org: "rancher", Name: "rancher", Category: "Kubernetes", Stars: 23000},
		{Org: "rancher", Name: "fleet", Category: "Kubernetes", Stars: 2000},
		{Org: "gravitational", Name: "gravity", Category: "Kubernetes", Stars: 3000},
		{Org: "ovh", Name: "vrack", Category: "Networking", Stars: 500},
		{Org: "tailscale", Name: "tailscale", Category: "TLS/Security", Stars: 20000},
		{Org: "netbirdio", Name: "netbird", Category: "TLS/Security", Stars: 12000},
		{Org: "firezone", Name: "firezone", Category: "TLS/Security", Stars: 7000},
		{Org: "wireguard", Name: "wireguard-go", Category: "TLS/Security", Stars: 3000},
		{Org: "junegunn", Name: "fzf", Category: "Go Tools", Stars: 67000},
		{Org: "junegunn", Name: "go-runewidth", Category: "Go Tools", Stars: 400},
		{Org: "lotusirous", Name: "go-concurrency", Category: "Go Tools", Stars: 3000},
		{Org: "uber-go", Name: "guide", Category: "Go Tools", Stars: 16000},
		{Org: "golang-design", Name: "go2generics", Category: "Go Tools", Stars: 2000},
		{Org: "golang", Name: "go", Category: "Go Core", Stars: 125000},
		{Org: "golang", Name: "crypto", Category: "TLS/Security", Stars: 3000},
		{Org: "golang", Name: "net", Category: "TLS/Security", Stars: 3000},
		{Org: "golang", Name: "sys", Category: "Go Core", Stars: 2000},
		{Org: "golang", Name: "tools", Category: "Go Core", Stars: 7000},
		{Org: "golang", Name: "mod", Category: "Go Core", Stars: 1000},
		{Org: "golang", Name: "sync", Category: "Go Core", Stars: 1000},
		{Org: "golang", Name: "text", Category: "Go Core", Stars: 1500},
		{Org: "golang", Name: "exp", Category: "Go Core", Stars: 2000},
		{Org: "golang", Name: "vuln", Category: "TLS/Security", Stars: 3000},
		{Org: "golang", Name: "time", Category: "Go Core", Stars: 500},
		{Org: "etcd-io", Name: "etcd", Category: "TLS/Security", Stars: 46000},
		{Org: "etcd-io", Name: "raft", Category: "Go Tools", Stars: 1000},
		{Org: "etcd-io", Name: "gofail", Category: "Go Tools", Stars: 300},
		{Org: "etcd-io", Name: "bbolt", Category: "Go Tools", Stars: 8000},
		{Org: "syndtr", Name: "goleveldb", Category: "Go Tools", Stars: 6000},
		{Org: "dgraph-io", Name: "badger", Category: "Go Tools", Stars: 14000},
		{Org: "blevesearch", Name: "bleve", Category: "Go Tools", Stars: 11000},
		{Org: "machadovilaca", Name: "operator-builder", Category: "Kubernetes", Stars: 200},
		{Org: "operator-framework", Name: "operator-lifecycle-manager", Category: "Kubernetes", Stars: 3000},

		// Kubernetes Baremetal / On-Premise
		{Org: "kubernetes-sigs", Name: "kubespray", Category: "Baremetal", Stars: 16000},
		{Org: "kubernetes", Name: "minikube", Category: "Baremetal", Stars: 29000},
		{Org: "kubernetes-sigs", Name: "kubeadm", Category: "Baremetal", Stars: 7000},

		// Networking / CNI
		{Org: "projectcalico", Name: "calico", Category: "Networking", Stars: 6000},
		{Org: "flannel-io", Name: "flannel", Category: "Networking", Stars: 9000},
		{Org: "kube-router", Name: "kube-router", Category: "Networking", Stars: 2000},
		{Org: "kubernetes-sigs", Name: "gateway-api", Category: "Networking", Stars: 2000},

		// Container Runtime
		{Org: "opencontainers", Name: "runc", Category: "Container Runtime", Stars: 12000},

		// Service Mesh
		{Org: "kumahq", Name: "kuma", Category: "Service Mesh", Stars: 6000},

		// Storage
		{Org: "openebs", Name: "openebs", Category: "Storage", Stars: 8000},
		{Org: "rancher", Name: "local-path-provisioner", Category: "Storage", Stars: 2000},
		{Org: "ceph", Name: "ceph", Category: "Storage", Stars: 4000},

		// Infrastructure
		{Org: "hashicorp", Name: "packer", Category: "Infrastructure", Stars: 15000},
		{Org: "hashicorp", Name: "vagrant", Category: "Infrastructure", Stars: 26000},

		// Backup
		{Org: "restic", Name: "restic", Category: "Backup", Stars: 26000},
		{Org: "kopia", Name: "kopia", Category: "Backup", Stars: 8000},
	}

	f.projects = excludeCategories(f.projects)

	sort.Slice(f.projects, func(i, j int) bool {
		return f.projects[i].Stars > f.projects[j].Stars
	})
}

// excludedCategories are scanned by nobody on a Go/DevOps track. They were the
// real source of the noise: only the top 30 projects by stars get checked each
// run, and the ML/AI entries (transformers 140k stars, langchain 100k,
// pytorch 85k) outrank every CNCF project, so they crowded the scan out and
// produced Python alerts no scoring tweak could fix.
// Override with SCAN_CATEGORIES_EXCLUDE (comma separated, empty to disable).
func excludedCategories() map[string]bool {
	raw, ok := os.LookupEnv("SCAN_CATEGORIES_EXCLUDE")
	if !ok {
		raw = "ML/AI"
	}
	out := map[string]bool{}
	for _, c := range strings.Split(raw, ",") {
		if c = strings.TrimSpace(strings.ToLower(c)); c != "" {
			out[c] = true
		}
	}
	return out
}

func excludeCategories(in []Project) []Project {
	drop := excludedCategories()
	if len(drop) == 0 {
		return in
	}
	kept := make([]Project, 0, len(in))
	for _, p := range in {
		if drop[strings.ToLower(p.Category)] {
			continue
		}
		kept = append(kept, p)
	}
	if n := len(in) - len(kept); n > 0 {
		log.Printf("[Scan] excluded %d projects by category (%d remain)", n, len(kept))
	}
	return kept
}

func (f *IssueFinder) FindIssues(ctx context.Context) ([]Issue, error) {
	var allIssues []Issue
	var mu sync.Mutex
	var projectWg sync.WaitGroup
	var collectorWg sync.WaitGroup

	issuesChan := make(chan Issue, 100)

	collectorWg.Add(1)
	go func() {
		defer collectorWg.Done()
		for issue := range issuesChan {
			mu.Lock()
			allIssues = append(allIssues, issue)
			mu.Unlock()
		}
	}()

	batchSize := 20
	maxProjects := 50
	projectsToCheck := f.projects
	if len(projectsToCheck) > maxProjects {
		projectsToCheck = f.projects[:maxProjects]
		log.Printf("[Rate Limit] Processing %d projects (of %d total)", maxProjects, len(f.projects))
	}

	for i := 0; i < len(projectsToCheck); i += batchSize {
		end := i + batchSize
		if end > len(projectsToCheck) {
			end = len(projectsToCheck)
		}

		log.Printf("[Rate Limit] Processing batch %d-%d of %d projects", i+1, end, len(projectsToCheck))

		for j := i; j < end; j++ {
			project := projectsToCheck[j]
			projectWg.Add(1)
			go func(p Project) {
				defer projectWg.Done()

				// An archived repo is read-only: it cannot accept a pull
				// request at all, so any issue in it is unworkable no matter
				// how good it looks. Learned the hard way on
				// containrrr/watchtower#2121 - the bug was real and the fix
				// was written and tested before the PR came back
				// "Repository was archived so is read-only".
				if f.isArchived(ctx, p.Org, p.Name) {
					log.Printf("Skipping %s/%s: repository is archived (read-only)", p.Org, p.Name)
					return
				}

				log.Printf("Checking issues for %s/%s (%d stars)", p.Org, p.Name, p.Stars)

				var issues []*github.Issue
				var err error

				err = f.rateLimiter.executeWithRetry(ctx, fmt.Sprintf("fetch issues for %s/%s", p.Org, p.Name), func() (*github.Response, error) {
					opts := &github.IssueListByRepoOptions{
						State:     "open",
						Sort:      "created",
						Direction: "desc",
						ListOptions: github.ListOptions{
							PerPage: f.config.MaxIssuesPerRepo,
						},
					}

					var apiErr error
					issues, _, apiErr = f.client.Issues.ListByRepo(ctx, p.Org, p.Name, opts)
					return nil, apiErr
				})

				if err != nil {
					log.Printf("Error fetching issues for %s/%s: %v", p.Org, p.Name, err)
					return
				}

				log.Printf("Found %d issues for %s/%s", len(issues), p.Org, p.Name)

				issuesAdded := 0
				for _, issue := range issues {
					if issue.IsPullRequest() {
						continue
					}

					if len(issue.Assignees) > 0 {
						continue
					}

					if issue.GetState() == "closed" {
						continue
					}

					issueID := fmt.Sprintf("%s/%d", p.Name, *issue.Number)

					f.mu.RLock()
					seen := f.seenIssues[issueID]
					f.mu.RUnlock()

					if seen {
						continue
					}

					score := f.scorer.ScoreIssue(issue, p)

					labels := make([]string, 0, len(issue.Labels))
					for _, label := range issue.Labels {
						labels = append(labels, label.GetName())
					}

					isGoodFirst := false
					for _, label := range issue.Labels {
						if strings.Contains(strings.ToLower(label.GetName()), "good first issue") {
							isGoodFirst = true
							break
						}
					}

					newIssue := Issue{
						Project:     p,
						Title:       *issue.Title,
						URL:         *issue.HTMLURL,
						Number:      *issue.Number,
						Score:       score,
						CreatedAt:   issue.CreatedAt.Time,
						Comments:    *issue.Comments,
						Labels:      labels,
						Language:    "Go",
						IsGoodFirst: isGoodFirst,
					}

					issuesChan <- newIssue
					issuesAdded++

					if err := f.markIssueSeen(issueID, p.Name); err != nil {
						log.Printf("Error marking issue %s as seen: %v", issueID, err)
					}

					if err := f.saveIssueHistory(newIssue); err != nil {
						log.Printf("Error saving issue history: %v", err)
					}
				}
				log.Printf("Added %d new issues from %s/%s", issuesAdded, p.Org, p.Name)
			}(project)
		}

		projectWg.Wait()

		if end < len(projectsToCheck) {
			log.Printf("[Rate Limit] Batch complete, pausing briefly before next batch...")
			time.Sleep(500 * time.Millisecond)
		}
	}

	close(issuesChan)
	log.Printf("[Finder] Waiting for issue processors to finish...")
	collectorWg.Wait()
	log.Printf("[Finder] Processed %d total issues, sorting by score...", len(allIssues))

	sort.Slice(allIssues, func(i, j int) bool {
		return allIssues[i].Score > allIssues[j].Score
	})

	log.Printf("[Finder] Returning %d sorted issues", len(allIssues))
	return allIssues, nil
}

func (f *IssueFinder) FindGoodFirstIssues(ctx context.Context, categories []string) ([]Issue, error) {
	var allIssues []Issue
	var mu sync.Mutex
	var projectWg sync.WaitGroup
	var collectorWg sync.WaitGroup

	issuesChan := make(chan Issue, 100)

	collectorWg.Add(1)
	go func() {
		defer collectorWg.Done()
		for issue := range issuesChan {
			mu.Lock()
			allIssues = append(allIssues, issue)
			mu.Unlock()
		}
	}()

	categorySet := make(map[string]bool)
	for _, c := range categories {
		categorySet[strings.ToLower(c)] = true
	}

	var filteredProjects []Project
	for _, p := range f.projects {
		if len(categorySet) == 0 || categorySet[strings.ToLower(p.Category)] {
			filteredProjects = append(filteredProjects, p)
		}
	}

	maxProjects := 30
	if len(filteredProjects) > maxProjects {
		filteredProjects = filteredProjects[:maxProjects]
	}
	log.Printf("[Good First Issues] Checking %d projects in categories: %v", len(filteredProjects), categories)

	batchSize := 10
	for i := 0; i < len(filteredProjects); i += batchSize {
		end := i + batchSize
		if end > len(filteredProjects) {
			end = len(filteredProjects)
		}

		log.Printf("[Good First Issues] Processing batch %d-%d", i+1, end)

		for j := i; j < end; j++ {
			project := filteredProjects[j]
			projectWg.Add(1)
			go func(p Project) {
				defer projectWg.Done()

				var issues []*github.Issue
				var err error

				err = f.rateLimiter.executeWithRetry(ctx, fmt.Sprintf("fetch good first issues for %s/%s", p.Org, p.Name), func() (*github.Response, error) {
					opts := &github.IssueListByRepoOptions{
						State:     "open",
						Sort:      "created",
						Direction: "desc",
						Labels:    []string{"good first issue"},
						ListOptions: github.ListOptions{
							PerPage: 20,
						},
					}

					var apiErr error
					issues, _, apiErr = f.client.Issues.ListByRepo(ctx, p.Org, p.Name, opts)
					return nil, apiErr
				})

				if err != nil {
					log.Printf("Error fetching good first issues for %s/%s: %v", p.Org, p.Name, err)
					return
				}

				if len(issues) > 0 {
					log.Printf("Found %d good first issues for %s/%s", len(issues), p.Org, p.Name)
				}

				for _, issue := range issues {
					if issue.IsPullRequest() {
						continue
					}

					if len(issue.Assignees) > 0 {
						continue
					}

					if issue.GetState() == "closed" {
						continue
					}

					issueID := fmt.Sprintf("%s/%d", p.Name, *issue.Number)

					f.mu.RLock()
					seen := f.seenIssues[issueID]
					f.mu.RUnlock()

					if seen {
						continue
					}

					score := f.scorer.ScoreIssue(issue, p) + 0.3

					labels := make([]string, 0, len(issue.Labels))
					for _, label := range issue.Labels {
						labels = append(labels, label.GetName())
					}

					isGoodFirst := true

					newIssue := Issue{
						Project:     p,
						Title:       *issue.Title,
						URL:         *issue.HTMLURL,
						Number:      *issue.Number,
						Score:       score,
						CreatedAt:   issue.CreatedAt.Time,
						Comments:    *issue.Comments,
						Labels:      labels,
						Language:    "Go",
						IsGoodFirst: isGoodFirst,
					}

					issuesChan <- newIssue
				}
			}(project)
		}

		projectWg.Wait()
		time.Sleep(500 * time.Millisecond)
	}

	close(issuesChan)
	collectorWg.Wait()

	sort.Slice(allIssues, func(i, j int) bool {
		return allIssues[i].Score > allIssues[j].Score
	})

	log.Printf("[Good First Issues] Found %d issues", len(allIssues))
	return allIssues, nil
}

func PrintGoodFirstIssues(issues []Issue, title string) {
	fmt.Printf("\n%s\n", title)
	fmt.Println(strings.Repeat("=", 80))

	if len(issues) == 0 {
		fmt.Println("No good first issues found.")
		return
	}

	for i, issue := range issues {
		if i >= 30 {
			break
		}

		emoji := "🔥"
		if issue.Score < 0.8 {
			emoji = "⭐"
		}
		if issue.Score < 0.6 {
			emoji = "✨"
		}

		fmt.Printf("\n%s [%d] %s\n", emoji, i+1, issue.Title)
		fmt.Printf("   Score: %.2f | %s/%s (%d★) | Comments: %d\n", issue.Score, issue.Project.Org, issue.Project.Name, issue.Project.Stars, issue.Comments)
		fmt.Printf("   Category: %s\n", issue.Project.Category)
		fmt.Printf("   URL: %s\n", issue.URL)
		if len(issue.Labels) > 0 {
			fmt.Printf("   Labels: %s\n", strings.Join(issue.Labels, ", "))
		}
		fmt.Printf("   Created: %s\n", issue.CreatedAt.Format("2006-01-02"))
		fmt.Println(strings.Repeat("-", 80))
	}
}

func PrintIssuesByCategory(issues []Issue) {
	categories := make(map[string][]Issue)
	for _, issue := range issues {
		cat := issue.Project.Category
		categories[cat] = append(categories[cat], issue)
	}

	for cat, catIssues := range categories {
		sort.Slice(catIssues, func(i, j int) bool {
			return catIssues[i].Score > catIssues[j].Score
		})

		fmt.Printf("\n\nCategory: %s (%d issues)\n", cat, len(catIssues))
		fmt.Println(strings.Repeat("-", 80))

		for i, issue := range catIssues {
			if i >= 10 {
				break
			}

			emoji := "🔥"
			if issue.Score < 0.8 {
				emoji = "⭐"
			}
			if issue.Score < 0.6 {
				emoji = "✨"
			}

			fmt.Printf("%s [%d] %s (%.2f)\n", emoji, i+1, truncateString(issue.Title, 60), issue.Score)
			fmt.Printf("    %s/%s | %s\n", issue.Project.Org, issue.Project.Name, issue.URL)
		}
	}
}

func (f *IssueFinder) FindActionableIssues(ctx context.Context) ([]Issue, error) {
	actionableProjects := []Project{
		{Org: "golang", Name: "go", Category: "Go Core", Stars: 125000},
		{Org: "golang", Name: "crypto", Category: "TLS/Security", Stars: 3000},
		{Org: "golang", Name: "net", Category: "TLS/Security", Stars: 3000},
		{Org: "hashicorp", Name: "vault", Category: "TLS/Security", Stars: 29000},
		{Org: "hashicorp", Name: "consul", Category: "TLS/Security", Stars: 27000},
		{Org: "hashicorp", Name: "nomad", Category: "TLS/Security", Stars: 14000},
		{Org: "hashicorp", Name: "boundary", Category: "TLS/Security", Stars: 5000},
		{Org: "kubernetes", Name: "kubernetes", Category: "TLS/Security", Stars: 105000},
		{Org: "etcd-io", Name: "etcd", Category: "TLS/Security", Stars: 46000},
		{Org: "prometheus", Name: "prometheus", Category: "TLS/Security", Stars: 53000},
		{Org: "prometheus", Name: "alertmanager", Category: "TLS/Security", Stars: 6500},
		{Org: "grafana", Name: "grafana", Category: "TLS/Security", Stars: 58000},
		{Org: "grafana", Name: "loki", Category: "TLS/Security", Stars: 21000},
		{Org: "grafana", Name: "tempo", Category: "TLS/Security", Stars: 5500},
		{Org: "cilium", Name: "cilium", Category: "TLS/Security", Stars: 18000},
		{Org: "istio", Name: "istio", Category: "TLS/Security", Stars: 35000},
		{Org: "traefik", Name: "traefik", Category: "TLS/Security", Stars: 50000},
		{Org: "caddyserver", Name: "caddy", Category: "TLS/Security", Stars: 58000},
		{Org: "grpc", Name: "grpc-go", Category: "TLS/Security", Stars: 21000},
		{Org: "coredns", Name: "coredns", Category: "TLS/Security", Stars: 11000},
		{Org: "minio", Name: "minio", Category: "TLS/Security", Stars: 45000},
		{Org: "containerd", Name: "containerd", Category: "TLS/Security", Stars: 15000},
		{Org: "helm", Name: "helm", Category: "TLS/Security", Stars: 25000},
		{Org: "argoproj", Name: "argo-cd", Category: "TLS/Security", Stars: 15000},
		{Org: "argoproj", Name: "argo-workflows", Category: "TLS/Security", Stars: 14000},
		{Org: "fluxcd", Name: "flux2", Category: "TLS/Security", Stars: 6000},
		{Org: "dapr", Name: "dapr", Category: "TLS/Security", Stars: 24000},
		{Org: "open-telemetry", Name: "opentelemetry-go", Category: "TLS/Security", Stars: 4500},
		{Org: "open-telemetry", Name: "opentelemetry-collector", Category: "TLS/Security", Stars: 3500},
		{Org: "jaegertracing", Name: "jaeger", Category: "TLS/Security", Stars: 19000},
		{Org: "cert-manager", Name: "cert-manager", Category: "TLS/Security", Stars: 12000},
		{Org: "tailscale", Name: "tailscale", Category: "TLS/Security", Stars: 20000},
		{Org: "stretchr", Name: "testify", Category: "Go Tools", Stars: 23000},
		{Org: "spf13", Name: "cobra", Category: "Go Tools", Stars: 38000},
		{Org: "spf13", Name: "viper", Category: "Go Tools", Stars: 27000},
		{Org: "gin-gonic", Name: "gin", Category: "Go Web", Stars: 77000},
		{Org: "labstack", Name: "echo", Category: "Go Web", Stars: 30000},
		{Org: "gorilla", Name: "mux", Category: "Go Web", Stars: 21000},
		{Org: "go-gorm", Name: "gorm", Category: "Go Tools", Stars: 37000},
		{Org: "go-redis", Name: "redis", Category: "TLS/Security", Stars: 20000},
	}

	var allIssues []Issue
	var mu sync.Mutex
	var projectWg sync.WaitGroup
	var collectorWg sync.WaitGroup

	issuesChan := make(chan Issue, 100)

	collectorWg.Add(1)
	go func() {
		defer collectorWg.Done()
		for issue := range issuesChan {
			mu.Lock()
			allIssues = append(allIssues, issue)
			mu.Unlock()
		}
	}()

	excludeKeywords := []string{"go 1.26", "go1.26", "golang 1.26", "go 1.27", "go1.27", "upgrade to go", "bump go version"}

	log.Printf("[Actionable] Searching %d TLS-enabled Go projects for actionable issues...", len(actionableProjects))

	batchSize := 10
	for i := 0; i < len(actionableProjects); i += batchSize {
		end := i + batchSize
		if end > len(actionableProjects) {
			end = len(actionableProjects)
		}

		log.Printf("[Actionable] Processing batch %d-%d", i+1, end)

		for j := i; j < end; j++ {
			project := actionableProjects[j]
			projectWg.Add(1)
			go func(p Project) {
				defer projectWg.Done()

				var issues []*github.Issue
				var err error

				err = f.rateLimiter.executeWithRetry(ctx, fmt.Sprintf("fetch issues for %s/%s", p.Org, p.Name), func() (*github.Response, error) {
					opts := &github.IssueListByRepoOptions{
						State:     "open",
						Sort:      "created",
						Direction: "desc",
						ListOptions: github.ListOptions{
							PerPage: 30,
						},
					}

					var apiErr error
					issues, _, apiErr = f.client.Issues.ListByRepo(ctx, p.Org, p.Name, opts)
					return nil, apiErr
				})

				if err != nil {
					log.Printf("Error fetching issues for %s/%s: %v", p.Org, p.Name, err)
					return
				}

				for _, issue := range issues {
					if issue.IsPullRequest() {
						continue
					}

					if len(issue.Assignees) > 0 {
						continue
					}

					if issue.GetState() == "closed" {
						continue
					}

					title := strings.ToLower(safeString(issue.Title))
					body := strings.ToLower(safeString(issue.Body))
					combinedText := title + " " + body

					isExcluded := false
					for _, kw := range excludeKeywords {
						if strings.Contains(combinedText, strings.ToLower(kw)) {
							isExcluded = true
							break
						}
					}
					if isExcluded {
						continue
					}

					labels := make([]string, 0, len(issue.Labels))
					hasGoodFirst := false
					hasHelpWanted := false
					hasBug := false
					hasEnhancement := false

					for _, label := range issue.Labels {
						labelName := strings.ToLower(label.GetName())
						labels = append(labels, label.GetName())
						if strings.Contains(labelName, "good first issue") {
							hasGoodFirst = true
						}
						if strings.Contains(labelName, "help wanted") {
							hasHelpWanted = true
						}
						if strings.Contains(labelName, "bug") {
							hasBug = true
						}
						if strings.Contains(labelName, "enhancement") || strings.Contains(labelName, "feature") {
							hasEnhancement = true
						}
					}

					if !hasGoodFirst && !hasHelpWanted && !hasBug && !hasEnhancement {
						continue
					}

					issueID := fmt.Sprintf("%s/%d", p.Name, *issue.Number)

					f.mu.RLock()
					seen := f.seenIssues[issueID]
					f.mu.RUnlock()

					if seen {
						continue
					}

					score := 0.6
					if hasGoodFirst {
						score += 0.25
					}
					if hasHelpWanted {
						score += 0.20
					}
					if hasBug {
						score += 0.10
					}
					if hasEnhancement {
						score += 0.05
					}
					if *issue.Comments <= 2 {
						score += 0.15
					} else if *issue.Comments <= 5 {
						score += 0.10
					}
					age := time.Since(issue.CreatedAt.Time).Hours()
					if age <= 72 {
						score += 0.15
					} else if age <= 168 {
						score += 0.10
					}

					if score > 1.5 {
						score = 1.5
					}

					newIssue := Issue{
						Project:     p,
						Title:       *issue.Title,
						URL:         *issue.HTMLURL,
						Number:      *issue.Number,
						Score:       score,
						CreatedAt:   issue.CreatedAt.Time,
						Comments:    *issue.Comments,
						Labels:      labels,
						Language:    "Go",
						IsGoodFirst: hasGoodFirst,
					}

					issuesChan <- newIssue
				}
			}(project)
		}

		projectWg.Wait()
		time.Sleep(500 * time.Millisecond)
	}

	close(issuesChan)
	collectorWg.Wait()

	sort.Slice(allIssues, func(i, j int) bool {
		return allIssues[i].Score > allIssues[j].Score
	})

	log.Printf("[Actionable] Found %d actionable issues", len(allIssues))
	return allIssues, nil
}

func PrintActionableIssues(issues []Issue) {
	fmt.Printf("\n%s\n", "ACTIONABLE ISSUES FROM TLS-ENABLED GO PROJECTS")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("(Good First Issues, Bugs, Enhancements - No Go 1.26 Required)")
	fmt.Println(strings.Repeat("-", 80))

	if len(issues) == 0 {
		fmt.Println("No actionable issues found.")
		return
	}

	fmt.Printf("\nTotal Found: %d issues\n\n", len(issues))

	goodFirstIssues := []Issue{}
	bugIssues := []Issue{}
	enhancementIssues := []Issue{}

	for _, issue := range issues {
		isGoodFirst := false
		isBug := false
		isEnhancement := false

		for _, label := range issue.Labels {
			labelLower := strings.ToLower(label)
			if strings.Contains(labelLower, "good first issue") {
				isGoodFirst = true
			}
			if strings.Contains(labelLower, "bug") {
				isBug = true
			}
			if strings.Contains(labelLower, "enhancement") || strings.Contains(labelLower, "feature") {
				isEnhancement = true
			}
		}

		if isGoodFirst {
			goodFirstIssues = append(goodFirstIssues, issue)
		} else if isBug {
			bugIssues = append(bugIssues, issue)
		} else if isEnhancement {
			enhancementIssues = append(enhancementIssues, issue)
		}
	}

	if len(goodFirstIssues) > 0 {
		fmt.Printf("\n🔥 GOOD FIRST ISSUES (%d issues)\n", len(goodFirstIssues))
		fmt.Println(strings.Repeat("-", 80))
		for i, issue := range goodFirstIssues {
			if i >= 15 {
				break
			}
			fmt.Printf("\n✅ [%d] %s (Score: %.2f)\n", i+1, issue.Title, issue.Score)
			fmt.Printf("   Project: %s/%s (%d★) | %s\n", issue.Project.Org, issue.Project.Name, issue.Project.Stars, issue.Project.Category)
			fmt.Printf("   Comments: %d | Created: %s\n", issue.Comments, issue.CreatedAt.Format("2006-01-02"))
			fmt.Printf("   URL: %s\n", issue.URL)
			if len(issue.Labels) > 0 {
				fmt.Printf("   Labels: %s\n", strings.Join(issue.Labels, ", "))
			}
		}
	}

	if len(bugIssues) > 0 {
		fmt.Printf("\n\n🐛 BUG ISSUES (%d issues)\n", len(bugIssues))
		fmt.Println(strings.Repeat("-", 80))
		for i, issue := range bugIssues {
			if i >= 10 {
				break
			}
			fmt.Printf("\n🔴 [%d] %s (Score: %.2f)\n", i+1, issue.Title, issue.Score)
			fmt.Printf("   Project: %s/%s (%d★) | %s\n", issue.Project.Org, issue.Project.Name, issue.Project.Stars, issue.Project.Category)
			fmt.Printf("   Comments: %d | Created: %s\n", issue.Comments, issue.CreatedAt.Format("2006-01-02"))
			fmt.Printf("   URL: %s\n", issue.URL)
		}
	}

	if len(enhancementIssues) > 0 {
		fmt.Printf("\n\n✨ ENHANCEMENT ISSUES (%d issues)\n", len(enhancementIssues))
		fmt.Println(strings.Repeat("-", 80))
		for i, issue := range enhancementIssues {
			if i >= 10 {
				break
			}
			fmt.Printf("\n🟢 [%d] %s (Score: %.2f)\n", i+1, issue.Title, issue.Score)
			fmt.Printf("   Project: %s/%s (%d★) | %s\n", issue.Project.Org, issue.Project.Name, issue.Project.Stars, issue.Project.Category)
			fmt.Printf("   Comments: %d | Created: %s\n", issue.Comments, issue.CreatedAt.Format("2006-01-02"))
			fmt.Printf("   URL: %s\n", issue.URL)
		}
	}
}

func (f *IssueFinder) FindGoUpgradeIssues(ctx context.Context) ([]Issue, error) {
	tlsProjects := []Project{
		{Org: "golang", Name: "go", Category: "Go Core", Stars: 125000},
		{Org: "golang", Name: "crypto", Category: "Go TLS", Stars: 3000},
		{Org: "golang", Name: "net", Category: "Go TLS", Stars: 3000},
		{Org: "gorilla", Name: "mux", Category: "Go Web", Stars: 21000},
		{Org: "gin-gonic", Name: "gin", Category: "Go Web", Stars: 77000},
		{Org: "labstack", Name: "echo", Category: "Go Web", Stars: 30000},
		{Org: "go-chi", Name: "chi", Category: "Go Web", Stars: 18000},
		{Org: "grpc", Name: "grpc-go", Category: "Go TLS", Stars: 21000},
		{Org: "etcd-io", Name: "etcd", Category: "Go TLS", Stars: 46000},
		{Org: "hashicorp", Name: "vault", Category: "Go TLS", Stars: 29000},
		{Org: "hashicorp", Name: "consul", Category: "Go TLS", Stars: 27000},
		{Org: "hashicorp", Name: "nomad", Category: "Go TLS", Stars: 14000},
		{Org: "kubernetes", Name: "kubernetes", Category: "Go TLS", Stars: 105000},
		{Org: "prometheus", Name: "prometheus", Category: "Go TLS", Stars: 53000},
		{Org: "prometheus", Name: "alertmanager", Category: "Go TLS", Stars: 6500},
		{Org: "grafana", Name: "loki", Category: "Go TLS", Stars: 21000},
		{Org: "grafana", Name: "tempo", Category: "Go TLS", Stars: 5500},
		{Org: "cilium", Name: "cilium", Category: "Go TLS", Stars: 18000},
		{Org: "linkerd", Name: "linkerd2", Category: "Go TLS", Stars: 10000},
		{Org: "istio", Name: "istio", Category: "Go TLS", Stars: 35000},
		{Org: "envoyproxy", Name: "gateway", Category: "Go TLS", Stars: 4000},
		{Org: "traefik", Name: "traefik", Category: "Go TLS", Stars: 50000},
		{Org: "caddyserver", Name: "caddy", Category: "Go TLS", Stars: 58000},
		{Org: "coredns", Name: "coredns", Category: "Go TLS", Stars: 11000},
		{Org: "mongodb", Name: "mongo-go-driver", Category: "Go TLS", Stars: 8000},
		{Org: "go-sql-driver", Name: "mysql", Category: "Go TLS", Stars: 14000},
		{Org: "lib", Name: "pq", Category: "Go TLS", Stars: 9000},
		{Org: "redis", Name: "go-redis", Category: "Go TLS", Stars: 20000},
		{Org: "go-redis", Name: "redis", Category: "Go TLS", Stars: 20000},
		{Org: "minio", Name: "minio", Category: "Go TLS", Stars: 45000},
		{Org: "docker", Name: "distribution", Category: "Go TLS", Stars: 9000},
		{Org: "containerd", Name: "containerd", Category: "Go TLS", Stars: 15000},
		{Org: "moby", Name: "moby", Category: "Go TLS", Stars: 68000},
		{Org: "opencontainers", Name: "runc", Category: "Go TLS", Stars: 12000},
		{Org: "helm", Name: "helm", Category: "Go TLS", Stars: 25000},
		{Org: "argoproj", Name: "argo-cd", Category: "Go TLS", Stars: 15000},
		{Org: "argoproj", Name: "argo-workflows", Category: "Go TLS", Stars: 14000},
		{Org: "fluxcd", Name: "flux2", Category: "Go TLS", Stars: 6000},
		{Org: "tektoncd", Name: "pipeline", Category: "Go TLS", Stars: 8000},
		{Org: "knative", Name: "serving", Category: "Go TLS", Stars: 5000},
		{Org: "knative", Name: "eventing", Category: "Go TLS", Stars: 4000},
		{Org: "dapr", Name: "dapr", Category: "Go TLS", Stars: 24000},
		{Org: "open-telemetry", Name: "opentelemetry-go", Category: "Go TLS", Stars: 4500},
		{Org: "open-telemetry", Name: "opentelemetry-collector", Category: "Go TLS", Stars: 3500},
		{Org: "jaegertracing", Name: "jaeger", Category: "Go TLS", Stars: 19000},
		{Org: "zalando", Name: "skipper", Category: "Go TLS", Stars: 3000},
		{Org: "projectcontour", Name: "contour", Category: "Go TLS", Stars: 3500},
		{Org: "k8s-io", Name: "ingress-nginx", Category: "Go TLS", Stars: 17000},
		{Org: "kubernetes", Name: "ingress-nginx", Category: "Go TLS", Stars: 17000},
		{Org: "oauth2-proxy", Name: "oauth2-proxy", Category: "Go TLS", Stars: 9000},
		{Org: "keycloak", Name: "keycloak", Category: "Go TLS", Stars: 22000},
	}

	var allIssues []Issue
	var mu sync.Mutex
	var projectWg sync.WaitGroup
	var collectorWg sync.WaitGroup

	issuesChan := make(chan Issue, 100)

	collectorWg.Add(1)
	go func() {
		defer collectorWg.Done()
		for issue := range issuesChan {
			mu.Lock()
			allIssues = append(allIssues, issue)
			mu.Unlock()
		}
	}()

	keywords := []string{"go 1.26", "golang 1.26", "go1.26", "upgrade go", "go version", "go 1.25", "golang 1.25", "go1.25", "go 1.27", "golang 1.27", "go1.27", "update go", "bump go", "go.mod"}

	log.Printf("[Go Upgrade] Searching %d TLS-enabled Go projects for Go version upgrade issues...", len(tlsProjects))

	batchSize := 10
	for i := 0; i < len(tlsProjects); i += batchSize {
		end := i + batchSize
		if end > len(tlsProjects) {
			end = len(tlsProjects)
		}

		log.Printf("[Go Upgrade] Processing batch %d-%d", i+1, end)

		for j := i; j < end; j++ {
			project := tlsProjects[j]
			projectWg.Add(1)
			go func(p Project) {
				defer projectWg.Done()

				var issues []*github.Issue
				var err error

				err = f.rateLimiter.executeWithRetry(ctx, fmt.Sprintf("fetch issues for %s/%s", p.Org, p.Name), func() (*github.Response, error) {
					opts := &github.IssueListByRepoOptions{
						State:     "open",
						Sort:      "created",
						Direction: "desc",
						ListOptions: github.ListOptions{
							PerPage: 30,
						},
					}

					var apiErr error
					issues, _, apiErr = f.client.Issues.ListByRepo(ctx, p.Org, p.Name, opts)
					return nil, apiErr
				})

				if err != nil {
					log.Printf("Error fetching issues for %s/%s: %v", p.Org, p.Name, err)
					return
				}

				for _, issue := range issues {
					if issue.IsPullRequest() {
						continue
					}

					if len(issue.Assignees) > 0 {
						continue
					}

					if issue.GetState() == "closed" {
						continue
					}

					title := strings.ToLower(safeString(issue.Title))
					body := strings.ToLower(safeString(issue.Body))
					combinedText := title + " " + body

					isGoUpgrade := false
					for _, kw := range keywords {
						if strings.Contains(combinedText, strings.ToLower(kw)) {
							isGoUpgrade = true
							break
						}
					}

					if !isGoUpgrade {
						continue
					}

					issueID := fmt.Sprintf("%s/%d", p.Name, *issue.Number)

					f.mu.RLock()
					seen := f.seenIssues[issueID]
					f.mu.RUnlock()

					if seen {
						continue
					}

					score := 0.85
					if strings.Contains(combinedText, "go 1.26") || strings.Contains(combinedText, "go1.26") {
						score = 1.0
					} else if strings.Contains(combinedText, "go 1.25") || strings.Contains(combinedText, "go1.25") {
						score = 0.95
					} else if strings.Contains(combinedText, "upgrade") || strings.Contains(combinedText, "bump") {
						score = 0.90
					}

					labels := make([]string, 0, len(issue.Labels))
					for _, label := range issue.Labels {
						labels = append(labels, label.GetName())
					}

					newIssue := Issue{
						Project:     p,
						Title:       *issue.Title,
						URL:         *issue.HTMLURL,
						Number:      *issue.Number,
						Score:       score,
						CreatedAt:   issue.CreatedAt.Time,
						Comments:    *issue.Comments,
						Labels:      labels,
						Language:    "Go",
						IsGoodFirst: false,
					}

					issuesChan <- newIssue
				}
			}(project)
		}

		projectWg.Wait()
		time.Sleep(500 * time.Millisecond)
	}

	close(issuesChan)
	collectorWg.Wait()

	sort.Slice(allIssues, func(i, j int) bool {
		return allIssues[i].Score > allIssues[j].Score
	})

	log.Printf("[Go Upgrade] Found %d Go version upgrade issues in TLS-enabled projects", len(allIssues))
	return allIssues, nil
}

func PrintGoUpgradeIssues(issues []Issue) {
	fmt.Printf("\n%s\n", "GO VERSION UPGRADE ISSUES IN TLS-ENABLED PROJECTS")
	fmt.Println(strings.Repeat("=", 80))

	if len(issues) == 0 {
		fmt.Println("No Go version upgrade issues found.")
		return
	}

	fmt.Printf("\nTotal Found: %d issues\n", len(issues))
	fmt.Println(strings.Repeat("-", 80))

	for i, issue := range issues {
		emoji := "🔥"
		if issue.Score < 0.90 {
			emoji = "⭐"
		}
		if issue.Score < 0.85 {
			emoji = "✨"
		}

		fmt.Printf("\n%s [%d] %s (Score: %.2f)\n", emoji, i+1, issue.Title, issue.Score)
		fmt.Printf("   Project: %s/%s (%d★) | Category: %s\n", issue.Project.Org, issue.Project.Name, issue.Project.Stars, issue.Project.Category)
		fmt.Printf("   Comments: %d | Created: %s\n", issue.Comments, issue.CreatedAt.Format("2006-01-02"))
		fmt.Printf("   URL: %s\n", issue.URL)
		if len(issue.Labels) > 0 {
			fmt.Printf("   Labels: %s\n", strings.Join(issue.Labels, ", "))
		}
		fmt.Println(strings.Repeat("-", 80))
	}
}

type ConfirmedGoodFirstIssue struct {
	Issue
	HasConfirmedLabel bool
	HasGoodFirstLabel bool
	HasLinkedPR       bool
	HasAssignee       bool
	IsEligible        bool
}

func (f *IssueFinder) FindConfirmedGoodFirstIssues(ctx context.Context, targetRepo string) ([]ConfirmedGoodFirstIssue, error) {
	var allIssues []ConfirmedGoodFirstIssue
	var mu sync.Mutex

	targetProjects := f.projects
	if targetRepo != "" {
		parts := strings.Split(targetRepo, "/")
		if len(parts) == 2 {
			targetProjects = []Project{}
			for _, p := range f.projects {
				if p.Org == parts[0] && p.Name == parts[1] {
					targetProjects = append(targetProjects, p)
					break
				}
			}
			if len(targetProjects) == 0 {
				targetProjects = []Project{{Org: parts[0], Name: parts[1], Category: "Unknown", Stars: 0}}
			}
		}
	}

	log.Printf("[Confirmed GFI] Checking %d projects for good first issue + confirmed labels", len(targetProjects))

	batchSize := 10
	for i := 0; i < len(targetProjects); i += batchSize {
		end := i + batchSize
		if end > len(targetProjects) {
			end = len(targetProjects)
		}

		var projectWg sync.WaitGroup

		for j := i; j < end; j++ {
			project := targetProjects[j]
			projectWg.Add(1)
			go func(p Project) {
				defer projectWg.Done()

				var issues []*github.Issue
				var err error

				err = f.rateLimiter.executeWithRetry(ctx, fmt.Sprintf("fetch confirmed GFI for %s/%s", p.Org, p.Name), func() (*github.Response, error) {
					opts := &github.IssueListByRepoOptions{
						State:     "open",
						Sort:      "created",
						Direction: "desc",
						Labels:    []string{"good first issue"},
						ListOptions: github.ListOptions{
							PerPage: 50,
						},
					}

					var apiErr error
					issues, _, apiErr = f.client.Issues.ListByRepo(ctx, p.Org, p.Name, opts)
					return nil, apiErr
				})

				if err != nil {
					log.Printf("Error fetching issues for %s/%s: %v", p.Org, p.Name, err)
					return
				}

				for _, issue := range issues {
					if issue.IsPullRequest() {
						continue
					}

					if issue.GetState() != "open" {
						continue
					}

					labels := make([]string, 0, len(issue.Labels))
					hasGoodFirst := hasGoodFirstIssueLabel(issue.Labels)
					hasConfirmed := hasConfirmedLabel(issue.Labels)

					for _, label := range issue.Labels {
						labels = append(labels, label.GetName())
					}

					hasAssignee := len(issue.Assignees) > 0
					hasPR := issue.PullRequestLinks != nil

					if !hasGoodFirst || !hasConfirmed {
						continue
					}

					if !hasPR {
						query := fmt.Sprintf("repo:%s/%s is:pr %d in:body", p.Org, p.Name, issue.GetNumber())
						result, _, err := f.client.Search.Issues(ctx, query, nil)
						if err == nil && result != nil {
							hasPR = *result.Total > 0
						}
					}

					isEligible := !hasAssignee && !hasPR

					score := f.scorer.ScoreIssue(issue, p) + 0.35
					if hasConfirmed {
						score += 0.25
					}
					if !hasAssignee {
						score += 0.15
					}
					if !hasPR {
						score += 0.10
					}

					age := time.Since(issue.CreatedAt.Time).Hours()
					if age > 168 && age < 2160 && *issue.Comments <= 3 {
						score += 0.10
					}

					if score > 1.5 {
						score = 1.5
					}

					confirmedIssue := ConfirmedGoodFirstIssue{
						Issue: Issue{
							Project:     p,
							Title:       issue.GetTitle(),
							URL:         issue.GetHTMLURL(),
							Number:      issue.GetNumber(),
							Score:       score,
							CreatedAt:   issue.GetCreatedAt().Time,
							Comments:    issue.GetComments(),
							Labels:      labels,
							Language:    "Go",
							IsGoodFirst: hasGoodFirst,
						},
						HasConfirmedLabel: hasConfirmed,
						HasGoodFirstLabel: hasGoodFirst,
						HasLinkedPR:       hasPR,
						HasAssignee:       hasAssignee,
						IsEligible:        isEligible,
					}

					mu.Lock()
					allIssues = append(allIssues, confirmedIssue)
					mu.Unlock()
				}
			}(project)
		}

		projectWg.Wait()
		time.Sleep(500 * time.Millisecond)
	}

	sort.Slice(allIssues, func(i, j int) bool {
		if allIssues[i].IsEligible != allIssues[j].IsEligible {
			return allIssues[i].IsEligible
		}
		return allIssues[i].Score > allIssues[j].Score
	})

	log.Printf("[Confirmed GFI] Found %d issues (eligible: %d)", len(allIssues), countEligible(allIssues))
	return allIssues, nil
}

func countEligible(issues []ConfirmedGoodFirstIssue) int {
	count := 0
	for _, issue := range issues {
		if issue.IsEligible {
			count++
		}
	}
	return count
}

func PrintConfirmedGoodFirstIssues(issues []ConfirmedGoodFirstIssue) {
	fmt.Printf("\n%s\n", "GOOD FIRST ISSUES WITH CONFIRMED LABEL (Ready for Assignment)")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("Criteria: good first issue + confirmed/triage/accepted, no assignee, no PR")
	fmt.Println(strings.Repeat("-", 80))

	if len(issues) == 0 {
		fmt.Println("No matching issues found.")
		return
	}

	var eligible []ConfirmedGoodFirstIssue
	var ineligible []ConfirmedGoodFirstIssue

	for _, issue := range issues {
		if issue.IsEligible {
			eligible = append(eligible, issue)
		} else {
			ineligible = append(ineligible, issue)
		}
	}

	if len(eligible) > 0 {
		fmt.Printf("\n✅ ELIGIBLE FOR ASSIGNMENT (%d issues)\n", len(eligible))
		fmt.Println(strings.Repeat("-", 80))
		for i, issue := range eligible {
			if i >= 20 {
				break
			}
			fmt.Printf("\n🔥 [%d] %s (Score: %.2f)\n", i+1, issue.Title, issue.Score)
			fmt.Printf("   Project: %s/%s (%d★)\n", issue.Project.Org, issue.Project.Name, issue.Project.Stars)
			fmt.Printf("   Comments: %d | Created: %s\n", issue.Comments, issue.CreatedAt.Format("2006-01-02"))
			fmt.Printf("   URL: %s\n", issue.URL)
			fmt.Printf("   Labels: %s\n", strings.Join(issue.Labels, ", "))
			fmt.Println(strings.Repeat("-", 80))
		}
	}

	if len(ineligible) > 0 {
		fmt.Printf("\n\n⚠️ NOT ELIGIBLE (%d issues)\n", len(ineligible))
		fmt.Println(strings.Repeat("-", 80))
		for i, issue := range ineligible {
			if i >= 10 {
				break
			}
			reason := ""
			if issue.HasAssignee {
				reason = "has assignee"
			} else if issue.HasLinkedPR {
				reason = "has linked PR"
			}
			fmt.Printf("\n[%d] %s (%s)\n", i+1, issue.Title, reason)
			fmt.Printf("   URL: %s\n", issue.URL)
		}
	}
}

// recordAlerted persists the fact that these issues were alerted on, into both
// the anti-spam log and the issue tracker.
//
// This used to live only in ProcessNewIssueNotifications, which runs under the
// manual `mode == "confirmed"` path and not in the scheduled loop that actually
// sends the Telegram alerts. The result was that after 339 issues seen,
// notification_log and tracked_issues were both still empty: WasAlreadyNotified
// could never return true, so the cooldown and the per-project notification
// limits in NotificationSpamManager were dead code in the deployed daemon.
// De-duplication only appeared to work because seen_issues is written earlier by
// a different mechanism.
//
// Failures are logged and skipped rather than aborting the loop - the alert has
// already gone out at this point, so the worst case is bookkeeping that lags,
// not a missed notification.
func (f *IssueFinder) recordAlerted(issues []Issue) {
	for _, issue := range issues {
		if f.antiSpam != nil {
			if err := f.antiSpam.RecordNotification(issue.Project.Name, issue.URL, issue.Number); err != nil {
				GetLogger().Warn("Failed to record notification for %s: %v", issue.URL, err)
			}
		}

		if f.tracker == nil {
			continue
		}

		// Issue carries plain string labels rather than the richer flags on
		// ConfirmedGoodFirstIssue, so the tracker fields are derived from what is
		// actually known at this point. HasAssignee and HasPR stay false on
		// purpose: dropTakenIssues ran immediately above and removed anything
		// with an open linked PR, so everything still here is unclaimed as far
		// as this pass could determine.
		hasConfirmed := false
		for _, l := range issue.Labels {
			switch strings.ToLower(strings.TrimSpace(l)) {
			case "triage/accepted", "confirmed", "status/confirmed", "kind/confirmed":
				hasConfirmed = true
			}
		}

		tracked := &TrackedIssue{
			IssueURL:     issue.URL,
			IssueTitle:   issue.Title,
			ProjectOrg:   issue.Project.Org,
			ProjectName:  issue.Project.Name,
			IssueNumber:  issue.Number,
			Status:       StatusNew,
			Score:        issue.Score,
			Labels:       strings.Join(issue.Labels, ","),
			HasGoodFirst: issue.IsGoodFirst,
			HasConfirmed: hasConfirmed,
		}
		if err := f.tracker.AddIssue(tracked); err != nil {
			GetLogger().Warn("Failed to track %s: %v", issue.URL, err)
			continue
		}
		if err := f.tracker.MarkNotified(issue.URL); err != nil {
			GetLogger().Warn("Failed to mark %s as notified: %v", issue.URL, err)
		}
	}
}

func (f *IssueFinder) ProcessNewIssueNotifications(ctx context.Context, issues []ConfirmedGoodFirstIssue) ([]Issue, error) {
	var newIssues []Issue

	for _, issue := range issues {
		if !issue.IsEligible {
			continue
		}

		issueURL := issue.URL

		if f.antiSpam != nil {
			alreadyNotified, err := f.antiSpam.WasAlreadyNotified(issueURL)
			if err != nil {
				log.Printf("Warning: failed to check notification status: %v", err)
			} else if alreadyNotified {
				continue
			}
		}

		if f.tracker != nil {
			wasNotified, err := f.tracker.WasNotified(issueURL)
			if err != nil {
				log.Printf("Warning: failed to check tracker notification status: %v", err)
			} else if wasNotified {
				continue
			}

			trackedIssue := &TrackedIssue{
				IssueURL:     issueURL,
				IssueTitle:   issue.Title,
				ProjectOrg:   issue.Project.Org,
				ProjectName:  issue.Project.Name,
				IssueNumber:  issue.Number,
				Status:       StatusNew,
				Score:        issue.Score,
				Labels:       strings.Join(issue.Labels, ","),
				HasGoodFirst: issue.HasGoodFirstLabel,
				HasConfirmed: issue.HasConfirmedLabel,
				HasAssignee:  issue.HasAssignee,
				HasPR:        issue.HasLinkedPR,
			}

			if err := f.tracker.AddIssue(trackedIssue); err != nil {
				log.Printf("Warning: failed to track issue: %v", err)
			}
		}

		newIssues = append(newIssues, issue.Issue)

		if f.antiSpam != nil {
			if err := f.antiSpam.RecordNotification(issue.Project.Name, issueURL, issue.Number); err != nil {
				log.Printf("Warning: failed to record notification: %v", err)
			}
		}

		if f.tracker != nil {
			if err := f.tracker.MarkNotified(issueURL); err != nil {
				log.Printf("Warning: failed to mark issue as notified: %v", err)
			}
		}
	}

	return newIssues, nil
}

// SendTelegramAlert returns the issues that Telegram actually accepted, so the
// caller only records those as notified. With no bot configured every issue
// counts as delivered: the local/email path still runs and the bookkeeping
// should not depend on Telegram being set up.
func (f *IssueFinder) SendTelegramAlert(issues []Issue) ([]Issue, error) {
	if f.bot == nil || len(issues) == 0 {
		return issues, nil
	}
	// Send each issue as its own message with convention-aware inline buttons
	// (Assign / Ask / PR) chosen per repo + your trust level there.
	return f.SendIssueAlertsWithButtons(issues)
}

func (f *IssueFinder) SendLocalAlert(issues []Issue) error {
	if f.notifier == nil {
		return nil
	}

	return f.notifier.SendIssuesAlert(issues)
}

func (f *IssueFinder) GetTopIssues(limit int) ([]Issue, error) {
	var issues []Issue

	query := `
		SELECT 
			ih.issue_title,
			ih.issue_url,
			ih.score,
			ih.comments,
			ih.labels,
			ih.created_at,
			ih.project_name,
			ih.category,
			COALESCE(p.stars, 0) as stars
		FROM issue_history ih
		LEFT JOIN (
			SELECT DISTINCT project_name, stars FROM (
				SELECT 
					CASE 
						WHEN project_name = 'kubernetes' THEN 'kubernetes'
						WHEN project_name = 'argo-cd' THEN 'argo-cd'
						WHEN project_name = 'prometheus' THEN 'prometheus'
						WHEN project_name = 'thanos' THEN 'thanos'
						WHEN project_name = 'flux2' THEN 'flux2'
						WHEN project_name = 'jaeger' THEN 'jaeger'
						WHEN project_name = 'opentelemetry-collector' THEN 'opentelemetry-collector'
						WHEN project_name = 'helm' THEN 'helm'
						WHEN project_name = 'velero' THEN 'velero'
						WHEN project_name = 'pipeline' THEN 'pipeline'
						WHEN project_name = 'cilium' THEN 'cilium'
						WHEN project_name = 'consul' THEN 'consul'
						WHEN project_name = 'vault' THEN 'vault'
						WHEN project_name = 'coredns' THEN 'coredns'
						WHEN project_name = 'containerd' THEN 'containerd'
						WHEN project_name = 'rook' THEN 'rook'
						WHEN project_name = 'grafana' THEN 'grafana'
						WHEN project_name = 'loki' THEN 'loki'
						WHEN project_name = 'tempo' THEN 'tempo'
						WHEN project_name = 'mimir' THEN 'mimir'
						WHEN project_name = 'k3s' THEN 'k3s'
						WHEN project_name = 'k9s' THEN 'k9s'
						WHEN project_name = 'trivy' THEN 'trivy'
						WHEN project_name = 'k6' THEN 'k6'
						WHEN project_name = 'skywalking' THEN 'skywalking'
						WHEN project_name = 'drone' THEN 'drone'
						WHEN project_name = 'podman' THEN 'podman'
						WHEN project_name = 'bazel' THEN 'bazel'
						WHEN project_name = 'jib' THEN 'jib'
						WHEN project_name = 'kaniko' THEN 'kaniko'
						WHEN project_name = 'buildah' THEN 'buildah'
						WHEN project_name = 'skaffold' THEN 'skaffold'
						WHEN project_name = 'tilt' THEN 'tilt'
						WHEN project_name = 'watchtower' THEN 'watchtower'
						WHEN project_name = 'prometheus-operator' THEN 'prometheus-operator'
						WHEN project_name = 'promtail' THEN 'promtail'
						WHEN project_name = 'grafana-agent' THEN 'grafana-agent'
						WHEN project_name = 'prometheus-lens' THEN 'prometheus-lens'
						WHEN project_name = 'thanos-receive-controller' THEN 'thanos-receive-controller'
						WHEN project_name = 'thanos-store' THEN 'thanos-store'
						WHEN project_name = 'thanos-query' THEN 'thanos-query'
						WHEN project_name = 'thanos-compact' THEN 'thanos-compact'
						WHEN project_name = 'thanos-rule' THEN 'thanos-rule'
						WHEN project_name = 'thanos-sidecar' THEN 'thanos-sidecar'
						WHEN project_name = 'thanos-bucket' THEN 'thanos-bucket'
						WHEN project_name = 'thanos-objstore' THEN 'thanos-objstore'
						WHEN project_name = 'cortex' THEN 'cortex'
						WHEN project_name = 'grafana-oncall' THEN 'grafana-oncall'
						WHEN project_name = 'grafana-phlare' THEN 'grafana-phlare'
						WHEN project_name = 'grafana-synthetic-monitoring-agent' THEN 'grafana-synthetic-monitoring-agent'
						WHEN project_name = 'grafana-faraday' THEN 'grafana-faraday'
						WHEN project_name = 'jaeger-query' THEN 'jaeger-query'
						WHEN project_name = 'jaeger-collector' THEN 'jaeger-collector'
						WHEN project_name = 'jaeger-agent' THEN 'jaeger-agent'
						WHEN project_name = 'jaeger-ingester' THEN 'jaeger-ingester'
						WHEN project_name = 'jaeger-all-in-one' THEN 'jaeger-all-in-one'
						WHEN project_name = 'zipkin' THEN 'zipkin'
						WHEN project_name = 'zipkin-ui' THEN 'zipkin-ui'
						WHEN project_name = 'zipkin-collector' THEN 'zipkin-collector'
						WHEN project_name = 'zipkin-query' THEN 'zipkin-query'
						WHEN project_name = 'zipkin-reporter' THEN 'zipkin-reporter'
						WHEN project_name = 'zipkin-storage' THEN 'zipkin-storage'
						WHEN project_name = 'zipkin-dependencies' THEN 'zipkin-dependencies'
						WHEN project_name = 'opentelemetry-collector-contrib' THEN 'opentelemetry-collector-contrib'
						WHEN project_name = 'opentelemetry-go' THEN 'opentelemetry-go'
						WHEN project_name = 'opentelemetry-java' THEN 'opentelemetry-java'
						WHEN project_name = 'opentelemetry-python' THEN 'opentelemetry-python'
						WHEN project_name = 'opentelemetry-js' THEN 'opentelemetry-js'
						WHEN project_name = 'opentelemetry-cpp' THEN 'opentelemetry-cpp'
						WHEN project_name = 'opentelemetry-rust' THEN 'opentelemetry-rust'
						WHEN project_name = 'opentelemetry-dotnet' THEN 'opentelemetry-dotnet'
						WHEN project_name = 'opentelemetry-php' THEN 'opentelemetry-php'
						WHEN project_name = 'opentelemetry-ruby' THEN 'opentelemetry-ruby'
						WHEN project_name = 'opentelemetry-erlang' THEN 'opentelemetry-erlang'
						WHEN project_name = 'opentelemetry-swift' THEN 'opentelemetry-swift'
						WHEN project_name = 'opentelemetry-kotlin' THEN 'opentelemetry-kotlin'
						WHEN project_name = 'opentelemetry-scala' THEN 'opentelemetry-scala'
						WHEN project_name = 'argo-workflows' THEN 'argo-workflows'
						WHEN project_name = 'argo-events' THEN 'argo-events'
						WHEN project_name = 'argo-rollouts' THEN 'argo-rollouts'
						WHEN project_name = 'argocd-image-updater' THEN 'argocd-image-updater'
						WHEN project_name = 'flux-operator' THEN 'flux-operator'
						WHEN project_name = 'helm-operator' THEN 'helm-operator'
						WHEN project_name = 'flux-helm-operator' THEN 'flux-helm-operator'
						WHEN project_name = 'flux-operator' THEN 'flux-operator'
						WHEN project_name = 'tekton-triggers' THEN 'tekton-triggers'
						WHEN project_name = 'tekton-cli' THEN 'tekton-cli'
						WHEN project_name = 'tekton-dashboard' THEN 'tekton-dashboard'
						WHEN project_name = 'tekton-catalog' THEN 'tekton-catalog'
						WHEN project_name = 'tekton-operator' THEN 'tekton-operator'
						WHEN project_name = 'tekton-results' THEN 'tekton-results'
						WHEN project_name = 'tekton-chains' THEN 'tekton-chains'
						WHEN project_name = 'tekton-hub' THEN 'tekton-hub'
						WHEN project_name = 'tekton-cd' THEN 'tekton-cd'
						WHEN project_name = 'dispatch' THEN 'dispatch'
						WHEN project_name = 'keel' THEN 'keel'
						WHEN project_name = 'gitlab-runner' THEN 'gitlab-runner'
						WHEN project_name = 'woodpecker' THEN 'woodpecker'
						WHEN project_name = 'gocd' THEN 'gocd'
						WHEN project_name = 'concourse' THEN 'concourse'
						WHEN project_name = 'screwdriver' THEN 'screwdriver'
						WHEN project_name = 'jenkins-x' THEN 'jenkins-x'
						WHEN project_name = 'lighthouse' THEN 'lighthouse'
						WHEN project_name = 'img' THEN 'img'
						WHEN project_name = 's2i' THEN 's2i'
						WHEN project_name = 'bazelisk' THEN 'bazelisk'
						WHEN project_name = 'please' THEN 'please'
						WHEN project_name = 'buck' THEN 'buck'
						WHEN project_name = 'meson' THEN 'meson'
						WHEN project_name = 'ninja' THEN 'ninja'
						WHEN project_name = 'keda' THEN 'keda'
						WHEN project_name = 'krew' THEN 'krew'
						WHEN project_name = 'kube-score' THEN 'kube-score'
						WHEN project_name = 'polaris' THEN 'polaris'
						WHEN project_name = 'goldilocks' THEN 'goldilocks'
						WHEN project_name = 'kube-linter' THEN 'kube-linter'
						WHEN project_name = 'pluto' THEN 'pluto'
						WHEN project_name = 'nozzle' THEN 'nozzle'
						WHEN project_name = 'rbac-lookup' THEN 'rbac-lookup'
						WHEN project_name = 'rbac-manager' THEN 'rbac-manager'
						WHEN project_name = 'vcluster' THEN 'vcluster'
						WHEN project_name = 'karmada' THEN 'karmada'
						WHEN project_name = 'liqo' THEN 'liqo'
						WHEN project_name = 'volcano' THEN 'volcano'
						WHEN project_name = 'kruise' THEN 'kruise'
						WHEN project_name = 'kubevela' THEN 'kubevela'
						WHEN project_name = 'oam-kubernetes-runtime' THEN 'oam-kubernetes-runtime'
						WHEN project_name = 'kubespray' THEN 'kubespray'
						WHEN project_name = 'minikube' THEN 'minikube'
						WHEN project_name = 'kubeadm' THEN 'kubeadm'
						WHEN project_name = 'calico' THEN 'calico'
						WHEN project_name = 'flannel' THEN 'flannel'
						WHEN project_name = 'kube-router' THEN 'kube-router'
						WHEN project_name = 'gateway-api' THEN 'gateway-api'
						WHEN project_name = 'openebs' THEN 'openebs'
						WHEN project_name = 'local-path-provisioner' THEN 'local-path-provisioner'
						WHEN project_name = 'ceph' THEN 'ceph'
						WHEN project_name = 'kuma' THEN 'kuma'
						WHEN project_name = 'packer' THEN 'packer'
						WHEN project_name = 'vagrant' THEN 'vagrant'
						WHEN project_name = 'restic' THEN 'restic'
						WHEN project_name = 'kopia' THEN 'kopia'
						WHEN project_name = 'dashboard' THEN 'dashboard'
						WHEN project_name = 'kube-state-metrics' THEN 'kube-state-metrics'
						WHEN project_name = 'node_exporter' THEN 'node_exporter'
						WHEN project_name = 'cadvisor' THEN 'cadvisor'
						WHEN project_name = 'blackbox_exporter' THEN 'blackbox_exporter'
						WHEN project_name = 'sql_exporter' THEN 'sql_exporter'
						WHEN project_name = 'jmx_exporter' THEN 'jmx_exporter'
						WHEN project_name = 'statsd_exporter' THEN 'statsd_exporter'
						WHEN project_name = 'smartping_exporter' THEN 'smartping_exporter'
						WHEN project_name = 'ping_exporter' THEN 'ping_exporter'
						WHEN project_name = 'cassandra_exporter' THEN 'cassandra_exporter'
						WHEN project_name = 'mongodb_exporter' THEN 'mongodb_exporter'
						WHEN project_name = 'redis_exporter' THEN 'redis_exporter'
						WHEN project_name = 'postgres_exporter' THEN 'postgres_exporter'
						WHEN project_name = 'mysqld_exporter' THEN 'mysqld_exporter'
						WHEN project_name = 'nginx-prometheus-exporter' THEN 'nginx-prometheus-exporter'
						WHEN project_name = 'haproxy_exporter' THEN 'haproxy_exporter'
						WHEN project_name = 'consul_exporter' THEN 'consul_exporter'
						WHEN project_name = 'etcd_exporter' THEN 'etcd_exporter'
						WHEN project_name = 'zookeeper_exporter' THEN 'zookeeper_exporter'
						WHEN project_name = 'kafka_exporter' THEN 'kafka_exporter'
						WHEN project_name = 'rabbitmq_exporter' THEN 'rabbitmq_exporter'
						WHEN project_name = 'thrift_exporter' THEN 'thrift_exporter'
						WHEN project_name = 'smtp_exporter' THEN 'smtp_exporter'
						WHEN project_name = 'http_exporter' THEN 'http_exporter'
						WHEN project_name = 'dns_exporter' THEN 'dns_exporter'
						WHEN project_name = 'imap_exporter' THEN 'imap_exporter'
						WHEN project_name = 'pop3_exporter' THEN 'pop3_exporter'
						WHEN project_name = 'ftp_exporter' THEN 'ftp_exporter'
						WHEN project_name = 'ssh_exporter' THEN 'ssh_exporter'
						WHEN project_name = 'collectd_exporter' THEN 'collectd_exporter'
						WHEN project_name = 'ganglia_exporter' THEN 'ganglia_exporter'
						WHEN project_name = 'influxdb_exporter' THEN 'influxdb_exporter'
						WHEN project_name = 'libvirt_exporter' THEN 'libvirt_exporter'
						WHEN project_name = 'mesos_exporter' THEN 'mesos_exporter'
						WHEN project_name = 'puppetdb_exporter' THEN 'puppetdb_exporter'
						WHEN project_name = 'riak_exporter' THEN 'riak_exporter'
						WHEN project_name = 'sensu_exporter' THEN 'sensu_exporter'
						WHEN project_name = 'puppet_exporter' THEN 'puppet_exporter'
						WHEN project_name = 'vault_exporter' THEN 'vault_exporter'
						WHEN project_name = 'fluentd_exporter' THEN 'fluentd_exporter'
						WHEN project_name = 'logstash_exporter' THEN 'logstash_exporter'
						WHEN project_name = 'beats_exporter' THEN 'beats_exporter'
						WHEN project_name = 'prometheus-lens' THEN 'prometheus-lens'
						ELSE project_name
					END as project_name,
					CASE 
						WHEN project_name = 'kubernetes' THEN 105000
						WHEN project_name = 'argo-cd' THEN 15000
						WHEN project_name = 'prometheus' THEN 53000
						WHEN project_name = 'thanos' THEN 12000
						WHEN project_name = 'flux2' THEN 6000
						WHEN project_name = 'jaeger' THEN 19000
						WHEN project_name = 'opentelemetry-collector' THEN 3500
						WHEN project_name = 'helm' THEN 25000
						WHEN project_name = 'velero' THEN 8000
						WHEN project_name = 'pipeline' THEN 8000
						WHEN project_name = 'cilium' THEN 18000
						WHEN project_name = 'consul' THEN 27000
						WHEN project_name = 'vault' THEN 29000
						WHEN project_name = 'coredns' THEN 11000
						WHEN project_name = 'containerd' THEN 15000
						WHEN project_name = 'rook' THEN 12000
						WHEN project_name = 'grafana' THEN 58000
						WHEN project_name = 'loki' THEN 21000
						WHEN project_name = 'tempo' THEN 5500
						WHEN project_name = 'mimir' THEN 4000
						WHEN project_name = 'k3s' THEN 25000
						WHEN project_name = 'k9s' THEN 24000
						WHEN project_name = 'trivy' THEN 21000
						WHEN project_name = 'k6' THEN 21000
						WHEN project_name = 'skywalking' THEN 22000
						WHEN project_name = 'drone' THEN 28000
						WHEN project_name = 'podman' THEN 20000
						WHEN project_name = 'bazel' THEN 21000
						WHEN project_name = 'jib' THEN 13000
						WHEN project_name = 'kaniko' THEN 13000
						WHEN project_name = 'buildah' THEN 7000
						WHEN project_name = 'skaffold' THEN 15000
						WHEN project_name = 'tilt' THEN 9000
						WHEN project_name = 'watchtower' THEN 17000
						WHEN project_name = 'prometheus-operator' THEN 8500
						WHEN project_name = 'promtail' THEN 1500
						WHEN project_name = 'grafana-agent' THEN 1500
						WHEN project_name = 'prometheus-lens' THEN 100
						WHEN project_name = 'thanos-receive-controller' THEN 50
						WHEN project_name = 'thanos-store' THEN 50
						WHEN project_name = 'thanos-query' THEN 50
						WHEN project_name = 'thanos-compact' THEN 50
						WHEN project_name = 'thanos-rule' THEN 50
						WHEN project_name = 'thanos-sidecar' THEN 50
						WHEN project_name = 'thanos-bucket' THEN 50
						WHEN project_name = 'thanos-objstore' THEN 50
						WHEN project_name = 'cortex' THEN 5000
						WHEN project_name = 'grafana-oncall' THEN 4000
						WHEN project_name = 'grafana-phlare' THEN 3000
						WHEN project_name = 'grafana-synthetic-monitoring-agent' THEN 200
						WHEN project_name = 'grafana-faraday' THEN 500
						WHEN project_name = 'jaeger-query' THEN 50
						WHEN project_name = 'jaeger-collector' THEN 50
						WHEN project_name = 'jaeger-agent' THEN 50
						WHEN project_name = 'jaeger-ingester' THEN 50
						WHEN project_name = 'jaeger-all-in-one' THEN 50
						WHEN project_name = 'zipkin' THEN 2000
						WHEN project_name = 'zipkin-ui' THEN 50
						WHEN project_name = 'zipkin-collector' THEN 50
						WHEN project_name = 'zipkin-query' THEN 50
						WHEN project_name = 'zipkin-reporter' THEN 50
						WHEN project_name = 'zipkin-storage' THEN 50
						WHEN project_name = 'zipkin-dependencies' THEN 50
						WHEN project_name = 'opentelemetry-collector-contrib' THEN 2000
						WHEN project_name = 'opentelemetry-go' THEN 4500
						WHEN project_name = 'opentelemetry-java' THEN 3000
						WHEN project_name = 'opentelemetry-python' THEN 2000
						WHEN project_name = 'opentelemetry-js' THEN 1500
						WHEN project_name = 'opentelemetry-cpp' THEN 800
						WHEN project_name = 'opentelemetry-rust' THEN 1200
						WHEN project_name = 'opentelemetry-dotnet' THEN 1000
						WHEN project_name = 'opentelemetry-php' THEN 500
						WHEN project_name = 'opentelemetry-ruby' THEN 300
						WHEN project_name = 'opentelemetry-erlang' THEN 100
						WHEN project_name = 'opentelemetry-swift' THEN 200
						WHEN project_name = 'opentelemetry-kotlin' THEN 100
						WHEN project_name = 'opentelemetry-scala' THEN 100
						WHEN project_name = 'argo-workflows' THEN 14000
						WHEN project_name = 'argo-events' THEN 2000
						WHEN project_name = 'argo-rollouts' THEN 2000
						WHEN project_name = 'argocd-image-updater' THEN 900
						WHEN project_name = 'flux-operator' THEN 500
						WHEN project_name = 'helm-operator' THEN 1000
						WHEN project_name = 'flux-helm-operator' THEN 1000
						WHEN project_name = 'flux-operator' THEN 500
						WHEN project_name = 'tekton-triggers' THEN 1000
						WHEN project_name = 'tekton-cli' THEN 600
						WHEN project_name = 'tekton-dashboard' THEN 400
						WHEN project_name = 'tekton-catalog' THEN 300
						WHEN project_name = 'tekton-operator' THEN 300
						WHEN project_name = 'tekton-results' THEN 200
						WHEN project_name = 'tekton-chains' THEN 200
						WHEN project_name = 'tekton-hub' THEN 200
						WHEN project_name = 'tekton-cd' THEN 300
						WHEN project_name = 'dispatch' THEN 500
						WHEN project_name = 'keel' THEN 2500
						WHEN project_name = 'gitlab-runner' THEN 4500
						WHEN project_name = 'woodpecker' THEN 4500
						WHEN project_name = 'gocd' THEN 7000
						WHEN project_name = 'concourse' THEN 7500
						WHEN project_name = 'screwdriver' THEN 1200
						WHEN project_name = 'jenkins-x' THEN 500
						WHEN project_name = 'lighthouse' THEN 200
						WHEN project_name = 'img' THEN 3000
						WHEN project_name = 's2i' THEN 2000
						WHEN project_name = 'bazelisk' THEN 1000
						WHEN project_name = 'please' THEN 1500
						WHEN project_name = 'buck' THEN 8000
						WHEN project_name = 'meson' THEN 3000
						WHEN project_name = 'ninja' THEN 3000
						WHEN project_name = 'keda' THEN 7500
						WHEN project_name = 'krew' THEN 5500
						WHEN project_name = 'kube-score' THEN 3000
						WHEN project_name = 'polaris' THEN 2500
						WHEN project_name = 'goldilocks' THEN 2000
						WHEN project_name = 'kube-linter' THEN 2000
						WHEN project_name = 'pluto' THEN 1200
						WHEN project_name = 'nozzle' THEN 400
						WHEN project_name = 'rbac-lookup' THEN 900
						WHEN project_name = 'rbac-manager' THEN 900
						WHEN project_name = 'vcluster' THEN 4500
						WHEN project_name = 'karmada' THEN 3500
						WHEN project_name = 'liqo' THEN 800
						WHEN project_name = 'volcano' THEN 3000
						WHEN project_name = 'kruise' THEN 4500
						WHEN project_name = 'kubevela' THEN 5500
						WHEN project_name = 'oam-kubernetes-runtime' THEN 1000
						WHEN project_name = 'kubespray' THEN 16000
						WHEN project_name = 'minikube' THEN 29000
						WHEN project_name = 'kubeadm' THEN 7000
						WHEN project_name = 'calico' THEN 6000
						WHEN project_name = 'flannel' THEN 9000
						WHEN project_name = 'kube-router' THEN 2000
						WHEN project_name = 'gateway-api' THEN 2000
						WHEN project_name = 'openebs' THEN 8000
						WHEN project_name = 'local-path-provisioner' THEN 2000
						WHEN project_name = 'ceph' THEN 4000
						WHEN project_name = 'kuma' THEN 6000
						WHEN project_name = 'packer' THEN 15000
						WHEN project_name = 'vagrant' THEN 26000
						WHEN project_name = 'restic' THEN 26000
						WHEN project_name = 'kopia' THEN 8000
						WHEN project_name = 'dashboard' THEN 13000
						WHEN project_name = 'kube-state-metrics' THEN 5000
						WHEN project_name = 'node_exporter' THEN 10000
						WHEN project_name = 'cadvisor' THEN 16000
						WHEN project_name = 'blackbox_exporter' THEN 4000
						WHEN project_name = 'sql_exporter' THEN 400
						WHEN project_name = 'jmx_exporter' THEN 3000
						WHEN project_name = 'statsd_exporter' THEN 1200
						WHEN project_name = 'smartping_exporter' THEN 200
						WHEN project_name = 'ping_exporter' THEN 250
						WHEN project_name = 'cassandra_exporter' THEN 300
						WHEN project_name = 'mongodb_exporter' THEN 600
						WHEN project_name = 'redis_exporter' THEN 4000
						WHEN project_name = 'postgres_exporter' THEN 2000
						WHEN project_name = 'mysqld_exporter' THEN 2000
						WHEN project_name = 'nginx-prometheus-exporter' THEN 1500
						WHEN project_name = 'haproxy_exporter' THEN 1000
						WHEN project_name = 'consul_exporter' THEN 400
						WHEN project_name = 'etcd_exporter' THEN 200
						WHEN project_name = 'zookeeper_exporter' THEN 200
						WHEN project_name = 'kafka_exporter' THEN 2000
						WHEN project_name = 'rabbitmq_exporter' THEN 500
						WHEN project_name = 'thrift_exporter' THEN 50
						WHEN project_name = 'smtp_exporter' THEN 50
						WHEN project_name = 'http_exporter' THEN 200
						WHEN project_name = 'dns_exporter' THEN 200
						WHEN project_name = 'imap_exporter' THEN 50
						WHEN project_name = 'pop3_exporter' THEN 50
						WHEN project_name = 'ftp_exporter' THEN 50
						WHEN project_name = 'ssh_exporter' THEN 100
						WHEN project_name = 'collectd_exporter' THEN 400
						WHEN project_name = 'ganglia_exporter' THEN 200
						WHEN project_name = 'influxdb_exporter' THEN 200
						WHEN project_name = 'libvirt_exporter' THEN 300
						WHEN project_name = 'mesos_exporter' THEN 100
						WHEN project_name = 'puppetdb_exporter' THEN 200
						WHEN project_name = 'riak_exporter' THEN 100
						WHEN project_name = 'sensu_exporter' THEN 100
						WHEN project_name = 'puppet_exporter' THEN 100
						WHEN project_name = 'vault_exporter' THEN 400
						WHEN project_name = 'fluentd_exporter' THEN 300
						WHEN project_name = 'logstash_exporter' THEN 200
						WHEN project_name = 'beats_exporter' THEN 200
						ELSE 0
					END as stars
				) p
			) p ON ih.project_name = p.project_name
		WHERE ih.created_at >= NOW() - INTERVAL '30 days'
		ORDER BY ih.score DESC
		LIMIT $1
	`

	err := f.db.Select(&issues, query, limit)
	if err != nil {
		return nil, err
	}

	return issues, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func runMonitorStatusOnly() error {
	fmt.Println("\n📊 MONITOR STATUS")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("   Running: No active daemon")
	fmt.Println("   Note: Use 'monitor start' to begin monitoring")
	fmt.Println("\n📋 Default Configuration:")
	config := DefaultMonitorConfig()
	fmt.Printf("   Check Interval: %v\n", config.CheckInterval)
	fmt.Printf("   Min Score: %.2f\n", config.MinScore)
	fmt.Printf("   Max Issues Per Check: %d\n", config.MaxIssuesPerCheck)
	fmt.Printf("   Notifications: Local=%v, Email=%v\n", config.NotifyLocal, config.NotifyEmail)
	fmt.Printf("   Repositories: %d\n", len(config.Repos))

	fmt.Println("\n📁 Monitored Repositories (by category):")
	categories := make(map[string][]RepoConfig)
	for _, repo := range config.Repos {
		categories[repo.Category] = append(categories[repo.Category], repo)
	}
	for cat, repos := range categories {
		fmt.Printf("\n   %s (%d repos):\n", strings.Title(cat), len(repos))
		for _, r := range repos {
			fmt.Printf("      - %s/%s (priority: %d)\n", r.Owner, r.Name, r.Priority)
		}
	}

	return nil
}

func main() {
	log.Printf("=== main() starting ===")
	cmd, args := ParseCLIArgs()

	if cmd == CmdMCP {
		if err := runMCPCommand(args); err != nil {
			log.Fatalf("Error: %v", err)
		}
		return
	}

	if cmd == CmdMCPHTTP {
		if err := runMCPHTTPCommand(args); err != nil {
			log.Fatalf("Error: %v", err)
		}
		return
	}

	if cmd == CmdMCPListTools {
		if err := runMCPListToolsCommand(args); err != nil {
			log.Fatalf("Error: %v", err)
		}
		return
	}

	if cmd == CmdMCPTest {
		if err := runMCPTestCommand(args); err != nil {
			log.Fatalf("Error: %v", err)
		}
		return
	}

	if cmd == CmdLimits {
		if err := runLimitsCommand(nil); err != nil {
			log.Printf("Error: %v", err)
			os.Exit(1)
		}
		return
	}

	if cmd == CmdMonitor {
		if len(args) == 0 {
			PrintMonitorUsage()
			return
		}
		subCmd := args[0]
		switch subCmd {
		case "status":
			runMonitorStatusOnly()
		case "start":
			fmt.Println("Start command requires a running service. Use 'monitor check' for one-time check.")
		case "stop":
			fmt.Println("Stop command requires a running service.")
		case "check":
			token := os.Getenv("GITHUB_TOKEN")
			if token == "" {
				fmt.Println("This command requires GITHUB_TOKEN to be set.")
				fmt.Println("Set GITHUB_TOKEN and try again.")
				return
			}

			ts := oauth2.StaticTokenSource(
				&oauth2.Token{AccessToken: token},
			)
			tc := oauth2.NewClient(context.Background(), ts)
			client := github.NewClient(tc)

			storage, err := NewFileStorage("")
			if err != nil {
				log.Printf("Error creating storage: %v", err)
				os.Exit(1)
			}

			monitorConfig := &MonitorConfig{
				Repos:             DefaultMonitorRepos,
				CheckInterval:     time.Hour,
				MinScore:          0.50,
				MaxIssuesPerCheck: 5,
				NotifyLocal:       true,
				NotifyEmail:       false,
			}

			monitor, err := NewIssueMonitor(monitorConfig, client, nil, storage)
			if err != nil {
				log.Printf("Error creating monitor: %v", err)
				os.Exit(1)
			}

			issues, err := monitor.CheckOnce(context.Background())
			if err != nil {
				log.Printf("Error: %v", err)
				os.Exit(1)
			}

			if len(issues) == 0 {
				fmt.Println("No new issues found matching criteria.")
			} else {
				fmt.Printf("\nFound %d new issues!\n\n", len(issues))
				for i, issue := range issues {
					fmt.Printf("%d. [%s] #%d - %s\n", i+1, issue.Repo, issue.IssueNumber, issue.Title)
					fmt.Printf("   Score: %.2f | %s\n\n", issue.Score, issue.URL)
				}

				monitor.NotifyLocal(issues)
			}

		case "notify":
			testIssues := []FoundIssue{
				{Repo: "test/repo", IssueNumber: 123, Title: "Test Issue", Score: 0.85, URL: "https://github.com/test/repo/issues/123"},
			}
			storage, err := NewFileStorage("")
			if err != nil {
				log.Printf("Error creating storage: %v", err)
				return
			}
			token := os.Getenv("GITHUB_TOKEN")
			if token == "" {
				token = "dummy"
			}
			ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
			tc := oauth2.NewClient(context.Background(), ts)
			client := github.NewClient(tc)

			config := &MonitorConfig{NotifyLocal: true}
			monitor, err := NewIssueMonitor(config, client, nil, storage)
			if err != nil {
				log.Printf("Error creating monitor: %v", err)
				return
			}
			monitor.NotifyLocal(testIssues)
			fmt.Println("Test notification sent!")
		default:
			fmt.Printf("Unknown monitor subcommand: %s\n", subCmd)
			PrintMonitorUsage()
		}
		return
	}

	chatID := int64(683539779)
	if chatEnv := os.Getenv("TELEGRAM_CHAT_ID"); chatEnv != "" {
		parsed, err := strconv.ParseInt(chatEnv, 10, 64)
		if err != nil {
			log.Printf("Invalid TELEGRAM_CHAT_ID value %q: %v", chatEnv, err)
		} else {
			chatID = parsed
		}
	}
	if chatEnv := os.Getenv("TELEGRAM_CHAT_IDS"); chatEnv != "" {
		parts := strings.Split(chatEnv, ",")
		if len(parts) > 0 {
			parsed, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
			if err == nil {
				chatID = parsed
			}
			if len(parts) > 1 {
				log.Printf("Multiple Telegram chats configured: %d chats; routing to primary %d", len(parts), chatID)
			}
		}
	}

	checkInterval := 3600
	if intervalEnv := os.Getenv("CHECK_INTERVAL"); intervalEnv != "" {
		parsed, err := strconv.Atoi(intervalEnv)
		if err != nil || parsed <= 0 {
			log.Printf("Invalid CHECK_INTERVAL %q, using default %d seconds", intervalEnv, checkInterval)
		} else {
			checkInterval = parsed
		}
	}

	maxIssues := 10
	if maxEnv := os.Getenv("MAX_ISSUES_PER_REPO"); maxEnv != "" {
		parsed, err := strconv.Atoi(maxEnv)
		if err != nil || parsed <= 0 {
			log.Printf("Invalid MAX_ISSUES_PER_REPO %q, using default %d", maxEnv, maxIssues)
		} else {
			maxIssues = parsed
		}
	}

	dbConn := os.Getenv("DB_CONNECTION_STRING")
	if dbConn == "" {
		dbConn = os.Getenv("DATABASE_URL")
	}
	if dbConn == "" {
		dbConn = "host=localhost user=postgres password=postgres dbname=issue_finder sslmode=disable port=5432"
	}

	emailConfig := loadEmailConfigFromEnv()
	if emailConfig == nil {
		log.Printf("Email notifications disabled: SMTP configuration incomplete")
	} else {
		log.Printf("Email notifications enabled for %s via %s", emailConfig.ToEmail, emailConfig.SMTPHost)
	}

	config := &Config{
		GitHubToken:        os.Getenv("GITHUB_TOKEN"),
		TelegramBotToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:     chatID,
		TelegramChatIDs:    parseTelegramChatIDs(os.Getenv("TELEGRAM_CHAT_ID"), os.Getenv("TELEGRAM_CHAT_IDS")),
		CheckInterval:      checkInterval,
		MaxIssuesPerRepo:   maxIssues,
		DBConnectionString: dbConn,
		Email:              emailConfig,
	}

	if config.GitHubToken == "" {
		log.Fatal("GITHUB_TOKEN environment variable is required")
	}

	if config.TelegramBotToken == "" {
		log.Printf("TELEGRAM_BOT_TOKEN not set, Telegram notifications disabled")
	}

	notifier, err := NewLocalNotifier(emailConfig)
	if err != nil {
		log.Printf("Failed to initialize local notifier: %v", err)
	}
	defer notifier.Close()

	finder, err := NewIssueFinder(config, notifier)
	if err != nil {
		log.Fatalf("Failed to create IssueFinder: %v", err)
	}
	defer finder.db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Printf("Received shutdown signal, stopping...")
		cancel()
	}()

	GetLogger().Info("Starting GitHub Issue Finder...")
	GetLogger().Info("Checking %d projects for good learning issues", min(len(finder.projects), 30))
	GetLogger().Info("Main initialization complete, entering main loop")

	runCheck := func() {
		GetLogger().Info("Running issue check...")
		if err := finder.rateLimiter.checkRateLimit(ctx); err != nil {
			GetLogger().Warn("Warning: failed to check rate limit: %v", err)
		}
		issues, err := finder.FindIssues(ctx)
		if err != nil {
			GetLogger().Warn("Error finding issues: %v", err)
			return
		}

		GetLogger().Info("Found %d new issues", len(issues))

		// The scorer marks disqualified issues with a negative score (cloud-only,
		// frontend-only, and so on). Without this filter they were still alerted:
		// kubernetes#141200 went out at score -1.00 because nothing acted on it.
		issues = filterAlertable(issues)

		// Last gate before anything is sent: drop issues someone is already
		// fixing. Every genuinely good issue in one 28-alert batch turned out
		// to be taken - dapr#10310 had PR #10311 opened the same day, k9s#4145
		// had two PRs, traefik#13643 had #13644 - and the alert still said
		// "Strong claim, open a PR". The check existed but only ran inside the
		// qualified finder, which is not the path that sends Telegram alerts.
		//
		// Deliberately after filterAlertable: this is one timeline call per
		// issue, so it only runs on the handful that survived the cap.
		issues = finder.dropTakenIssues(ctx, issues)

		if len(issues) == 0 {
			GetLogger().Info("No new issues found")
			return
		}

		GetLogger().Info("Sending alerts for %d issues...", len(issues))

		delivered, err := finder.SendTelegramAlert(issues)
		if err != nil {
			GetLogger().Warn("Error sending Telegram alert: %v", err)
		} else if finder.bot != nil {
			GetLogger().Info("Successfully sent Telegram alert for %d issues", len(delivered))
		}

		// Record only what Telegram actually accepted. Anything it rejected stays
		// unrecorded on purpose, so the next cycle retries it rather than
		// suppressing it as already notified.
		finder.recordAlerted(delivered)

		GetLogger().Info("Sending local/email alerts...")
		if err := finder.SendLocalAlert(issues); err != nil {
			GetLogger().Warn("Error processing local/email alert: %v", err)
		} else if config.Email != nil {
			GetLogger().Info("Email/local alert delivered for %d issues", len(issues))
		} else {
			GetLogger().Info("Logged %d issues locally (email disabled)", len(issues))
		}
		GetLogger().Info("Alert processing complete")
	}

	runGoodFirstIssues := func() {
		log.Printf("\n=== FINDING GOOD FIRST ISSUES ===")
		log.Printf("Searching for 'good first issue' labeled issues in CNCF, DevOps, ML/AI projects...")

		if err := finder.rateLimiter.checkRateLimit(ctx); err != nil {
			log.Printf("Warning: failed to check rate limit: %v", err)
		}

		goodFirstIssues, err := finder.FindGoodFirstIssues(ctx, []string{"Kubernetes", "Monitoring", "CI/CD", "ML/AI"})
		if err != nil {
			log.Printf("Error finding good first issues: %v", err)
			return
		}

		PrintGoodFirstIssues(goodFirstIssues, "GOOD FIRST ISSUES FROM CNCF, DEVOPS, ML/AI PROJECTS")

		PrintIssuesByCategory(goodFirstIssues)

		if notifier != nil {
			notifier.logToFile(fmt.Sprintf("Found %d good first issues", len(goodFirstIssues)))
			for _, issue := range goodFirstIssues {
				if issue.Score >= 0.7 {
					notifier.logToNotificationsFile(issue.Title, issue.URL, issue.Score, "Good First Issue")
				}
			}
		}
	}

	mode := os.Getenv("MODE")
	if mode == "good-first" {
		runGoodFirstIssues()
		return
	}

	if mode == "actionable" {
		log.Printf("\n=== FINDING ACTIONABLE ISSUES (No Go 1.26 Required) ===")
		log.Printf("Searching for good first issues, bugs, and enhancements from TLS-enabled Go projects...")
		if err := finder.rateLimiter.checkRateLimit(ctx); err != nil {
			log.Printf("Warning: failed to check rate limit: %v", err)
		}
		actionableIssues, err := finder.FindActionableIssues(ctx)
		if err != nil {
			log.Printf("Error finding actionable issues: %v", err)
			return
		}

		goodFirstIssues := []Issue{}
		otherIssues := []Issue{}
		for _, issue := range actionableIssues {
			if issue.IsGoodFirst {
				goodFirstIssues = append(goodFirstIssues, issue)
			} else {
				otherIssues = append(otherIssues, issue)
			}
		}
		DisplayPartitionedIssues(goodFirstIssues, otherIssues, []Issue{})

		if notifier != nil {
			notifier.logToFile(fmt.Sprintf("Found %d actionable issues", len(actionableIssues)))
			for _, issue := range actionableIssues {
				notifier.logToNotificationsFile(issue.Title, issue.URL, issue.Score, "Actionable")
			}
		}
		return
	}

	if mode == "partitioned" {
		log.Printf("\n=== FINDING ISSUES WITH PARTITIONED DISPLAY ===")
		if err := finder.rateLimiter.checkRateLimit(ctx); err != nil {
			log.Printf("Warning: failed to check rate limit: %v", err)
		}

		allIssues, err := finder.FindActionableIssues(ctx)
		if err != nil {
			log.Printf("Error finding issues: %v", err)
			return
		}

		goodFirstIssues := []Issue{}
		otherIssues := []Issue{}
		for _, issue := range allIssues {
			if issue.IsGoodFirst {
				goodFirstIssues = append(goodFirstIssues, issue)
			} else {
				otherIssues = append(otherIssues, issue)
			}
		}

		assignedIssues := []Issue{}
		assignedIssuesResp, _, err := finder.client.Search.Issues(ctx, "assignee:mehrdadbn9 state:open", nil)
		if err == nil && assignedIssuesResp != nil {
			for _, ghIssue := range assignedIssuesResp.Issues {
				issue := Issue{
					Title:     *ghIssue.Title,
					URL:       *ghIssue.HTMLURL,
					Number:    *ghIssue.Number,
					CreatedAt: ghIssue.CreatedAt.Time,
					Comments:  *ghIssue.Comments,
					Score:     0.0,
				}
				if ghIssue.Labels != nil {
					for _, label := range ghIssue.Labels {
						if label.Name != nil {
							issue.Labels = append(issue.Labels, *label.Name)
						}
					}
				}
				assignedIssues = append(assignedIssues, issue)
			}
		}

		DisplayPartitionedIssues(goodFirstIssues, otherIssues, assignedIssues)
		return
	}

	if mode == "go-upgrade" {
		log.Printf("\n=== FINDING GO VERSION UPGRADE ISSUES IN TLS-ENABLED PROJECTS ===")
		if err := finder.rateLimiter.checkRateLimit(ctx); err != nil {
			log.Printf("Warning: failed to check rate limit: %v", err)
		}
		goUpgradeIssues, err := finder.FindGoUpgradeIssues(ctx)
		if err != nil {
			log.Printf("Error finding Go upgrade issues: %v", err)
			return
		}
		PrintGoUpgradeIssues(goUpgradeIssues)
		if notifier != nil {
			notifier.logToFile(fmt.Sprintf("Found %d Go upgrade issues in TLS projects", len(goUpgradeIssues)))
			for _, issue := range goUpgradeIssues {
				notifier.logToNotificationsFile(issue.Title, issue.URL, issue.Score, "Go Upgrade")
			}
		}
		return
	}

	if mode == "confirmed" {
		log.Printf("\n=== FINDING CONFIRMED GOOD FIRST ISSUES ===")
		log.Printf("Searching for issues with 'good first issue' + 'confirmed/triage/accepted' labels...")

		if err := finder.rateLimiter.checkRateLimit(ctx); err != nil {
			log.Printf("Warning: failed to check rate limit: %v", err)
		}

		targetRepo := os.Getenv("TARGET_REPO")
		confirmedIssues, err := finder.FindConfirmedGoodFirstIssues(ctx, targetRepo)
		if err != nil {
			log.Printf("Error finding confirmed good first issues: %v", err)
			return
		}

		PrintConfirmedGoodFirstIssues(confirmedIssues)

		newIssues, err := finder.ProcessNewIssueNotifications(ctx, confirmedIssues)
		if err != nil {
			log.Printf("Error processing notifications: %v", err)
		}

		if len(newIssues) > 0 && finder.notifier != nil {
			log.Printf("Sending notifications for %d new issues...", len(newIssues))
			if err := finder.SendLocalAlert(newIssues); err != nil {
				log.Printf("Error sending local alert: %v", err)
			}
			if finder.bot != nil {
				if _, err := finder.SendTelegramAlert(newIssues); err != nil {
					log.Printf("Error sending Telegram alert: %v", err)
				}
			}
		}

		if finder.assignmentMgr != nil && finder.assignmentMgr.IsEnabled() {
			log.Printf("\n=== PROCESSING ASSIGNMENT REQUESTS ===")
			eligibleCount := 0
			for _, issue := range confirmedIssues {
				if issue.IsEligible {
					eligibleCount++
					if eligibleCount > 5 {
						break
					}
					candidate := &AssignmentCandidate{
						Issue: &github.Issue{
							Title:            github.String(issue.Title),
							Number:           github.Int(issue.Number),
							HTMLURL:          github.String(issue.URL),
							State:            github.String("open"),
							Assignees:        nil,
							Labels:           convertLabels(issue.Labels),
							PullRequestLinks: nil,
							CreatedAt:        &github.Timestamp{Time: issue.CreatedAt},
						},
						ProjectOrg:  issue.Project.Org,
						ProjectName: issue.Project.Name,
						Labels:      issue.Labels,
						HasPR:       issue.HasLinkedPR,
					}

					request, err := finder.assignmentMgr.AskForAssignment(ctx, candidate)
					if err != nil {
						log.Printf("Assignment request failed for %s#%d: %v", issue.Project.Name, issue.Number, err)
						continue
					}

					if request != nil {
						log.Printf("Assignment status for %s#%d: %s", issue.Project.Name, issue.Number, request.Status)
						if finder.tracker != nil {
							finder.tracker.MarkAssignmentAsked(issue.URL)
						}
					}
				}
			}
		}

		if notifier != nil {
			notifier.logToFile(fmt.Sprintf("Found %d confirmed good first issues", len(confirmedIssues)))
		}
		return
	}

	if mode == "both" {
		runCheck()
		fmt.Println()
		fmt.Println()
		runGoodFirstIssues()
		return
	}

	runCheck()

	ticker := time.NewTicker(time.Duration(config.CheckInterval) * time.Second)
	defer ticker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCheck()
			}
		}
	}()

	<-ctx.Done()
	log.Printf("Shutdown complete")
}
