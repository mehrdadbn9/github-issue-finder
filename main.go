package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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

	// Cloud provider penalty - user uses bare metal
	cloudKeywords := []string{
		"gcp", "google cloud", "compute engine", "gke", "cloud sql", "bigquery", "pubsub",
		"aws", "amazon web", "ec2", "s3 bucket", "lambda", "eks", "rds", "dynamodb",
		"azure", "microsoft azure", "aks", "azure functions", "azure storage",
	}
	if containsAny(combined, cloudKeywords) {
		score -= 0.50
	}

	if hasAnyLabel(issue.Labels, "provider:google", "provider:aws", "provider:azure", "area/gcp", "area/aws", "area/azure") {
		score -= 0.50
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

func containsAny(text string, keywords []string) bool {
	return slices.ContainsFunc(keywords, func(kw string) bool {
		return strings.Contains(text, kw)
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
	config      *Config
	client      *github.Client
	rateLimiter *RateLimiter
	bot         *tgbotapi.BotAPI
	notifier    *LocalNotifier
	db          *sqlx.DB
	scorer      *IssueScorer
	projects    []Project
	seenIssues  map[string]bool
	mu          sync.RWMutex
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

	db, err := sqlx.Connect("postgres", config.DBConnectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	client := github.NewClient(tc)
	rateLimiter := NewRateLimiter(client, 100)

	log.Printf("Initializing rate limiter with 100 request buffer...")
	if err := rateLimiter.checkRateLimit(ctx); err != nil {
		log.Printf("Warning: failed to fetch initial rate limits: %v", err)
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

	finder.initializeProjects()

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
		{Org: "prometheus", Name: "aws_cloudwatch_exporter", Category: "Monitoring", Stars: 400},
		{Org: "prometheus-community", Name: "stackdriver_exporter", Category: "Monitoring", Stars: 300},
		{Org: "prometheus-community", Name: "azure_exporter", Category: "Monitoring", Stars: 300},
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
	}

	sort.Slice(f.projects, func(i, j int) bool {
		return f.projects[i].Stars > f.projects[j].Stars
	})
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

func (f *IssueFinder) SendTelegramAlert(issues []Issue) error {
	if f.bot == nil {
		return nil
	}
	if len(issues) == 0 {
		return nil
	}

	var messages []string

	header := fmt.Sprintf("🚀 *New Learning Opportunities in Go DevOps Projects*\n\n")
	messages = append(messages, header)

	for i, issue := range issues {
		if i >= 20 {
			break
		}

		scoreEmoji := ""
		if issue.Score >= 0.8 {
			scoreEmoji = "🔥"
		} else if issue.Score >= 0.6 {
			scoreEmoji = "⭐"
		} else {
			scoreEmoji = "✨"
		}

		labelsText := ""
		if len(issue.Labels) > 0 {
			labelsText = fmt.Sprintf("\nLabels: %s", strings.Join(issue.Labels, ", "))
		}

		msg := fmt.Sprintf(
			"%s *%s* (%.2f)\n%s\n%s/%s (%d★)%s\n\n",
			scoreEmoji,
			truncateString(issue.Title, 80),
			issue.Score,
			issue.URL,
			issue.Project.Org,
			issue.Project.Name,
			issue.Project.Stars,
			labelsText,
		)

		messages = append(messages, msg)
	}

	for _, msg := range messages {
		tgMsg := tgbotapi.NewMessage(f.config.TelegramChatID, msg)
		tgMsg.ParseMode = "Markdown"

		_, err := f.bot.Send(tgMsg)
		if err != nil {
			log.Printf("Error sending Telegram message: %v", err)
			return err
		}

		time.Sleep(1 * time.Second)
	}

	return nil
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
						WHEN project_name = 'aws_cloudwatch_exporter' THEN 'aws_cloudwatch_exporter'
						WHEN project_name = 'stackdriver_exporter' THEN 'stackdriver_exporter'
						WHEN project_name = 'azure_exporter' THEN 'azure_exporter'
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
						WHEN project_name = 'aws_cloudwatch_exporter' THEN 400
						WHEN project_name = 'stackdriver_exporter' THEN 300
						WHEN project_name = 'azure_exporter' THEN 300
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

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func loadEmailConfigFromEnv() *EmailConfig {
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	username := strings.TrimSpace(os.Getenv("SMTP_USERNAME"))
	password := strings.TrimSpace(os.Getenv("SMTP_PASSWORD"))
	from := strings.TrimSpace(os.Getenv("FROM_EMAIL"))
	to := strings.TrimSpace(os.Getenv("TO_EMAIL"))
	port := strings.TrimSpace(os.Getenv("SMTP_PORT"))

	if host == "" || username == "" || password == "" || from == "" || to == "" {
		return nil
	}

	if port == "" {
		port = "587"
	}

	return &EmailConfig{
		SMTPHost:     host,
		SMTPPort:     port,
		SMTPUsername: username,
		SMTPPassword: password,
		FromEmail:    from,
		ToEmail:      to,
	}
}

func main() {
	chatID := int64(683539779)
	if chatEnv := os.Getenv("TELEGRAM_CHAT_ID"); chatEnv != "" {
		parsed, err := strconv.ParseInt(chatEnv, 10, 64)
		if err != nil {
			log.Printf("Invalid TELEGRAM_CHAT_ID value %q: %v", chatEnv, err)
		}
		chatID = parsed
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
		log.Printf("Failed to create IssueFinder: %v", err)
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

	log.Printf("Starting GitHub Issue Finder...")
	log.Printf("Checking %d projects for good learning issues", min(len(finder.projects), 30))

	runCheck := func() {
		log.Printf("Running issue check...")
		if err := finder.rateLimiter.checkRateLimit(ctx); err != nil {
			log.Printf("Warning: failed to check rate limit: %v", err)
		}
		issues, err := finder.FindIssues(ctx)
		if err != nil {
			log.Printf("Error finding issues: %v", err)
			return
		}

		log.Printf("Found %d new issues", len(issues))

		if len(issues) == 0 {
			log.Printf("No new issues found")
			return
		}

		log.Printf("Sending alerts for %d issues...", len(issues))

		if err := finder.SendTelegramAlert(issues); err != nil {
			log.Printf("Error sending Telegram alert: %v", err)
		} else if finder.bot != nil {
			log.Printf("Successfully sent Telegram alert for %d issues", len(issues))
		}

		log.Printf("Sending local/email alerts...")
		if err := finder.SendLocalAlert(issues); err != nil {
			log.Printf("Error processing local/email alert: %v", err)
		} else if config.Email != nil {
			log.Printf("Email/local alert delivered for %d issues", len(issues))
		} else {
			log.Printf("Logged %d issues locally (email disabled)", len(issues))
		}
		log.Printf("Alert processing complete")
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
		PrintActionableIssues(actionableIssues)
		if notifier != nil {
			notifier.logToFile(fmt.Sprintf("Found %d actionable issues", len(actionableIssues)))
			for _, issue := range actionableIssues {
				notifier.logToNotificationsFile(issue.Title, issue.URL, issue.Score, "Actionable")
			}
		}
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
