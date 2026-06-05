package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/go-github/v58/github"
	"github.com/jmoiron/sqlx"
	"golang.org/x/oauth2"
)

var logFile *os.File

// Default scoring weights (must sum to 1.0)
const (
	DefaultStarsWeight           float64 = 0.05
	DefaultCommentsWeight        float64 = 0.15
	DefaultRecencyWeight         float64 = 0.15
	DefaultLabelsWeight          float64 = 0.25
	DefaultDifficultyWeight      float64 = 0.15
	DefaultProjectPriorityWeight float64 = 0.25
)

type Config struct {
	StarsWeight        *float64 `json:"stars_weight,omitempty"`
	CommentsWeight     *float64 `json:"comments_weight,omitempty"`
	RecencyWeight      *float64 `json:"recency_weight,omitempty"`
	LabelsWeight       *float64 `json:"labels_weight,omitempty"`
	DifficultyWeight   *float64 `json:"difficulty_weight,omitempty"`
	PriorityWeight     *float64 `json:"priority_weight,omitempty"`
	GitHubToken        string   `json:"github_token"`
	GitHubUsername     string   `json:"github_username"`
	TelegramBotToken   string   `json:"telegram_bot_token"`
	TelegramChatID     int64    `json:"telegram_chat_id"`
	CheckInterval      int      `json:"check_interval"`
	MaxIssuesPerRepo   int      `json:"max_issues_per_repo"`
	MaxProjects        int      `json:"max_projects"`
	MaxRecommendations int      `json:"max_recommendations"`
	MaxPerProject      int      `json:"max_per_project"`
	MaxPerPriority     int      `json:"max_per_priority"`
	VerboseSkips       bool     `json:"verbose_skips"`
	VerifyLinkedPRs    bool     `json:"verify_linked_prs"`
	DBConnectionString string   `json:"db_connection_string"`
	LogDir             string   `json:"log_dir"`
}

type Project struct {
	Org      string
	Name     string
	Category string
	Stars    int
	Priority int
}

func (p Project) FullName() string {
	return p.Org + "/" + p.Name
}

type Issue struct {
	Project           Project
	Title             string
	URL               string
	Number            int
	Score             float64
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Comments          int
	Labels            []string
	Language          string
	IsGoodFirst       bool
	AssignedToMe      bool
	Recommendation    string
	ContributionState string
}

type IssueScorer struct {
	weights map[string]float64
}

func NewIssueScorer() *IssueScorer {
	return NewIssueScorerWithConfig(&Config{})
}

func NewIssueScorerWithConfig(config *Config) *IssueScorer {
	starsWeight := DefaultStarsWeight
	if config.StarsWeight != nil {
		starsWeight = *config.StarsWeight
	}

	commentsWeight := DefaultCommentsWeight
	if config.CommentsWeight != nil {
		commentsWeight = *config.CommentsWeight
	}

	recencyWeight := DefaultRecencyWeight
	if config.RecencyWeight != nil {
		recencyWeight = *config.RecencyWeight
	}

	labelsWeight := DefaultLabelsWeight
	if config.LabelsWeight != nil {
		labelsWeight = *config.LabelsWeight
	}

	difficultyWeight := DefaultDifficultyWeight
	if config.DifficultyWeight != nil {
		difficultyWeight = *config.DifficultyWeight
	}

	priorityWeight := DefaultProjectPriorityWeight
	if config.PriorityWeight != nil {
		priorityWeight = *config.PriorityWeight
	}

	total := starsWeight + commentsWeight + recencyWeight + labelsWeight + difficultyWeight + priorityWeight
	if total <= 0 {
		log.Printf("Warning: invalid scoring weights, falling back to defaults")
		starsWeight = DefaultStarsWeight
		commentsWeight = DefaultCommentsWeight
		recencyWeight = DefaultRecencyWeight
		labelsWeight = DefaultLabelsWeight
		difficultyWeight = DefaultDifficultyWeight
		priorityWeight = DefaultProjectPriorityWeight
		total = starsWeight + commentsWeight + recencyWeight + labelsWeight + difficultyWeight + priorityWeight
	}

	return &IssueScorer{
		weights: map[string]float64{
			"stars_factor":      starsWeight / total,
			"comments_factor":   commentsWeight / total,
			"recency_factor":    recencyWeight / total,
			"labels_factor":     labelsWeight / total,
			"difficulty_factor": difficultyWeight / total,
			"priority_factor":   priorityWeight / total,
		},
	}
}

func (s *IssueScorer) ScoreIssue(issue *github.Issue, project Project) float64 {
	var score float64

	starsScore := s.normalizeStars(project.Stars)
	score += starsScore * s.weights["stars_factor"]

	commentsScore := s.normalizeComments(issue.GetComments())
	score += commentsScore * s.weights["comments_factor"]

	recencyScore := s.normalizeRecency(issue.GetCreatedAt().Time)
	score += recencyScore * s.weights["recency_factor"]

	labelsScore := s.normalizeLabels(issue.Labels)
	score += labelsScore * s.weights["labels_factor"]

	difficultyScore := s.normalizeDifficulty(issue.Labels, issue.GetBody())
	score += difficultyScore * s.weights["difficulty_factor"]

	priorityScore := s.normalizePriority(project.Priority)
	score += priorityScore * s.weights["priority_factor"]

	return score
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
		return 1.0
	} else if comments <= 5 {
		return 0.7
	} else if comments <= 10 {
		return 0.4
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

	for _, label := range labels {
		labelName := strings.ToLower(label.GetName())

		if strings.Contains(labelName, "good first issue") ||
			strings.Contains(labelName, "help wanted") ||
			strings.Contains(labelName, "good-first") ||
			strings.Contains(labelName, "easy") ||
			strings.Contains(labelName, "beginner") ||
			strings.Contains(labelName, "starter") {
			score += 0.45
		}

		if strings.Contains(labelName, "bug") ||
			strings.Contains(labelName, "kind/bug") ||
			strings.Contains(labelName, "type/bug") ||
			strings.Contains(labelName, "defect") {
			score += 0.25
		}

		if strings.Contains(labelName, "needs-triage") ||
			strings.Contains(labelName, "triage") {
			score += 0.1
		}
	}

	if score > 1.0 {
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
		return 1.0
	}

	if strings.Contains(bodyLower, "simple") ||
		strings.Contains(bodyLower, "basic") ||
		strings.Contains(bodyLower, "small") ||
		strings.Contains(bodyLower, "straightforward") {
		return 0.75
	}

	if strings.Contains(bodyLower, "complex") ||
		strings.Contains(bodyLower, "difficult") ||
		strings.Contains(bodyLower, "challenging") {
		return 0.2
	}

	return 0.4
}

func (s *IssueScorer) normalizePriority(priority int) float64 {
	switch priority {
	case 1:
		return 1.0
	case 2:
		return 0.9
	case 3:
		return 0.8
	default:
		return 0.2
	}
}

type IssueFinder struct {
	config     *Config
	client     *github.Client
	bot        *tgbotapi.BotAPI
	db         *sqlx.DB
	hasDB      bool
	scorer     *IssueScorer
	projects   []Project
	seenIssues map[string]bool
	mu         sync.RWMutex
}

func NewIssueFinder(config *Config) (*IssueFinder, error) {
	finder := &IssueFinder{
		config:     config,
		scorer:     NewIssueScorerWithConfig(config),
		seenIssues: make(map[string]bool),
	}

	if config.GitHubToken != "" {
		ctx := context.Background()
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: config.GitHubToken},
		)
		tc := oauth2.NewClient(ctx, ts)
		finder.client = github.NewClient(tc)
		if config.GitHubUsername == "" {
			if user, _, err := finder.client.Users.Get(ctx, ""); err == nil {
				config.GitHubUsername = user.GetLogin()
				log.Printf("GitHub username detected as %s", config.GitHubUsername)
			} else {
				log.Printf("Warning: failed to detect GitHub username: %v", err)
			}
		}
	} else {
		finder.client = github.NewClient(nil)
	}

	if config.TelegramBotToken != "" {
		bot, err := createTelegramBot(config.TelegramBotToken)
		if err != nil {
			log.Printf("Warning: failed to create Telegram bot, running in console-only mode: %v", err)
		} else {
			finder.bot = bot
			log.Println("Telegram bot initialized successfully")
		}
	} else {
		log.Println("No Telegram bot token provided, running in console-only mode")
	}

	if config.DBConnectionString != "" {
		db, err := sqlx.Connect("postgres", config.DBConnectionString)
		if err != nil {
			log.Printf("Warning: failed to connect to database, running in memory-only mode: %v", err)
		} else {
			finder.db = db
			finder.hasDB = true
			if err := finder.initDB(); err != nil {
				log.Printf("Warning: failed to initialize database, running in memory-only mode: %v", err)
				finder.hasDB = false
			}
		}
	} else {
		log.Println("No database connection string provided, running in memory-only mode")
	}

	if finder.hasDB {
		if err := finder.loadSeenIssues(); err != nil {
			log.Printf("Warning: failed to load seen issues: %v", err)
		}
	}

	finder.initializeProjects()

	return finder, nil
}

func createTelegramBot(token string) (*tgbotapi.BotAPI, error) {
	return tgbotapi.NewBotAPI(token)
}

func (f *IssueFinder) initDB() error {
	if f.db == nil {
		return nil
	}
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
	if f.db == nil {
		return nil
	}
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

	if f.db == nil {
		return nil
	}

	_, err := f.db.Exec(`
		INSERT INTO seen_issues (issue_id, project_name, first_seen, last_notified)
		VALUES ($1, $2, $3, $3)
		ON CONFLICT (issue_id) DO UPDATE SET last_notified = $3
	`, issueID, projectName, time.Now())

	return err
}

func (f *IssueFinder) saveIssueHistory(issue Issue) error {
	if f.db == nil {
		return nil
	}

	labelsJSON, _ := json.Marshal(issue.Labels)

	_, err := f.db.Exec(`
		INSERT INTO issue_history (issue_id, issue_title, issue_url, project_name, category, score, comments, labels, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING
	`, f.issueID(issue.Project, issue.Number), issue.Title, issue.URL, issue.Project.Name, issue.Project.Category, issue.Score, issue.Comments, labelsJSON, issue.CreatedAt)

	return err
}

func (f *IssueFinder) issueID(project Project, number int) string {
	return fmt.Sprintf("%s#%d", project.FullName(), number)
}

func (f *IssueFinder) isCandidateIssue(issue *github.Issue) (bool, string) {
	if issue.IsPullRequest() {
		return false, "pull request"
	}

	if issue.GetLocked() {
		return false, "locked issue"
	}

	if issue.GetState() != "" && issue.GetState() != "open" {
		return false, "not open"
	}

	assignedToMe, assignedToOther := f.assignmentState(issue)
	if assignedToOther && !assignedToMe {
		return false, "assigned to someone else"
	}

	labels := lowerLabelNames(issue.Labels)
	if hasAnySubstring(labels, []string{
		"duplicate",
		"invalid",
		"wontfix",
		"won't fix",
		"not planned",
		"declined",
		"stale",
		"blocked",
		"needs info",
		"needs-info",
		"needs more information",
		"waiting for user",
		"question",
		"support",
		"discussion",
		"meta",
		"tracking",
		"umbrella",
		"epic",
		"rfc",
		"has_pr",
		"has-pr",
		"has pr",
		"pr exists",
		"in progress",
	}) {
		return false, "non-actionable label"
	}

	hasContributorLabel := hasAnySubstring(labels, []string{
		"good first issue",
		"good-first",
		"help wanted",
		"beginner",
		"easy",
		"starter",
	})
	hasRealIssueLabel := hasAnySubstring(labels, []string{
		"bug",
		"kind/bug",
		"type/bug",
		"defect",
		"regression",
		"flaky",
		"failure",
	})
	hasOnlyDocsLabel := hasAnySubstring(labels, []string{"documentation", "docs"}) && !hasRealIssueLabel && !hasContributorLabel
	if hasOnlyDocsLabel {
		return false, "docs-only issue"
	}

	text := strings.ToLower(issue.GetTitle() + "\n" + issue.GetBody())
	if looksLikeMaintenanceNotice(text) {
		return false, "maintenance/announcement issue"
	}

	if looksLikeSupportRequest(text) && !hasContributorLabel && !hasRealIssueLabel {
		return false, "support/discussion style issue"
	}

	if hasAnySubstring(labels, []string{"feature", "enhancement"}) && !hasContributorLabel && !hasRealIssueLabel {
		return false, "feature/enhancement without contributor signal"
	}

	if !hasContributorLabel && !hasRealIssueLabel && !looksLikeProblemReport(text) {
		return false, "not a clear actionable bug"
	}

	if strings.TrimSpace(issue.GetBody()) == "" && !hasContributorLabel && !hasRealIssueLabel {
		return false, "empty body without useful labels"
	}

	return true, ""
}

func (f *IssueFinder) assignmentState(issue *github.Issue) (assignedToMe bool, assignedToOther bool) {
	if len(issue.Assignees) == 0 {
		return false, false
	}

	username := strings.ToLower(strings.TrimSpace(f.config.GitHubUsername))
	for _, assignee := range issue.Assignees {
		login := strings.ToLower(assignee.GetLogin())
		if username != "" && login == username {
			assignedToMe = true
		} else {
			assignedToOther = true
		}
	}

	return assignedToMe, assignedToOther
}

func (f *IssueFinder) contributionState(issue *github.Issue) string {
	assignedToMe, assignedToOther := f.assignmentState(issue)
	if assignedToMe {
		return "assigned to you"
	}
	if assignedToOther {
		return "assigned to someone else"
	}
	return "unassigned"
}

func (f *IssueFinder) recommendationReason(issue *github.Issue, project Project) string {
	reasons := []string{}

	switch project.Priority {
	case 1:
		reasons = append(reasons, "KEDA focus repo")
	case 2:
		reasons = append(reasons, "Kubernetes/CNCF core repo")
	case 3:
		reasons = append(reasons, "Kafka/messaging repo")
	case 4:
		reasons = append(reasons, "monitoring/observability repo")
	case 5:
		reasons = append(reasons, "Ansible focus repo")
	case 6:
		reasons = append(reasons, "important CNCF repo")
	}

	labels := lowerLabelNames(issue.Labels)
	if hasAnySubstring(labels, []string{"good first issue", "good-first", "help wanted", "beginner", "easy", "starter"}) {
		reasons = append(reasons, "contributor-friendly label")
	}
	if hasAnySubstring(labels, []string{"bug", "kind/bug", "type/bug", "defect", "regression"}) {
		reasons = append(reasons, "real bug label")
	}
	if issue.GetComments() <= 2 {
		reasons = append(reasons, "low discussion/competition")
	}
	if state := f.contributionState(issue); state != "" {
		reasons = append(reasons, state)
	}

	if len(reasons) == 0 {
		return "passes contribution filters"
	}
	return strings.Join(reasons, "; ")
}

func (f *IssueFinder) logSkip(project Project, issueNumber int, reason string) {
	if !f.config.VerboseSkips {
		return
	}
	log.Printf("Skipping %s#%d: %s", project.FullName(), issueNumber, reason)
}

func lowerLabelNames(labels []*github.Label) []string {
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		names = append(names, strings.ToLower(label.GetName()))
	}
	return names
}

func hasAnySubstring(values []string, needles []string) bool {
	for _, value := range values {
		for _, needle := range needles {
			if strings.Contains(value, needle) {
				return true
			}
		}
	}
	return false
}

func looksLikeSupportRequest(text string) bool {
	supportTerms := []string{
		"how do i",
		"how can i",
		"question",
		"help me",
		"does anyone know",
		"feature request",
		"proposal",
		"discussion",
		"tracking issue",
		"umbrella issue",
		"rfc",
	}

	for _, term := range supportTerms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func looksLikeMaintenanceNotice(text string) bool {
	maintenanceTerms := []string{
		"dependency dashboard",
		"eol announcement",
		"end of life",
		"release checklist",
		"release tracking",
		"roadmap",
		"deprecation notice",
	}

	for _, term := range maintenanceTerms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func looksLikeProblemReport(text string) bool {
	problemTerms := []string{
		"bug",
		"regression",
		"fails",
		"failed",
		"failure",
		"error",
		"unable",
		"cannot",
		"can't",
		"not working",
		"panic",
		"crash",
		"timeout",
		"stuck",
		"missing",
		"incorrect",
		"wrong",
	}

	for _, term := range problemTerms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
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
	}

	f.prioritizeProjects()
}

func (f *IssueFinder) prioritizeProjects() {
	f.projects = append(f.projects,
		Project{Org: "apache", Name: "kafka", Category: "Messaging", Stars: 29000},
		Project{Org: "strimzi", Name: "strimzi-kafka-operator", Category: "Messaging", Stars: 5000},
		Project{Org: "rabbitmq", Name: "rabbitmq-server", Category: "Messaging", Stars: 12000},
		Project{Org: "rabbitmq", Name: "rabbitmq-management", Category: "Messaging", Stars: 1000},
		Project{Org: "rabbitmq", Name: "cluster-operator", Category: "Kubernetes", Stars: 900},
		Project{Org: "rabbitmq", Name: "messaging-topology-operator", Category: "Kubernetes", Stars: 400},
		Project{Org: "ansible", Name: "ansible", Category: "Automation", Stars: 65000},
		Project{Org: "ansible", Name: "ansible-lint", Category: "Automation", Stars: 4000},
		Project{Org: "etcd-io", Name: "etcd", Category: "Kubernetes", Stars: 49000},
		Project{Org: "envoyproxy", Name: "envoy", Category: "Kubernetes", Stars: 27000},
		Project{Org: "istio", Name: "istio", Category: "Kubernetes", Stars: 36000},
		Project{Org: "cert-manager", Name: "cert-manager", Category: "Kubernetes", Stars: 13000},
		Project{Org: "kubernetes-sigs", Name: "gateway-api", Category: "Kubernetes", Stars: 2500},
		Project{Org: "kubernetes-sigs", Name: "controller-runtime", Category: "Kubernetes", Stars: 3000},
		Project{Org: "open-telemetry", Name: "opentelemetry-operator", Category: "Monitoring", Stars: 1600},
		Project{Org: "prometheus-operator", Name: "kube-prometheus", Category: "Monitoring", Stars: 7000},
	)

	seen := make(map[string]bool, len(f.projects))
	deduped := make([]Project, 0, len(f.projects))
	for _, project := range f.projects {
		project.Priority = projectPriority(project)
		key := strings.ToLower(project.FullName())
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, project)
	}
	f.projects = deduped

	sort.Slice(f.projects, func(i, j int) bool {
		leftPriority := projectSortPriority(f.projects[i])
		rightPriority := projectSortPriority(f.projects[j])
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return f.projects[i].Stars > f.projects[j].Stars
	})
}

func projectPriority(project Project) int {
	fullName := strings.ToLower(project.FullName())
	name := strings.ToLower(project.Name)

	switch {
	case fullName == "kedacore/keda":
		return 1
	case isKubernetesCoreProject(fullName):
		return 2
	case isKafkaOrMessagingProject(fullName, name):
		return 3
	case isMonitoringProject(fullName):
		return 4
	case strings.HasPrefix(fullName, "ansible/"):
		return 5
	case isImportantCNCFProject(fullName):
		return 6
	case project.Priority > 0:
		return project.Priority
	default:
		return 50
	}
}

func projectSortPriority(project Project) int {
	return project.Priority
}

func isKubernetesCoreProject(fullName string) bool {
	coreProjects := map[string]bool{
		"kubernetes/kubernetes":                   true,
		"helm/helm":                               true,
		"cilium/cilium":                           true,
		"etcd-io/etcd":                            true,
		"containerd/containerd":                   true,
		"coredns/coredns":                         true,
		"envoyproxy/envoy":                        true,
		"istio/istio":                             true,
		"fluxcd/flux2":                            true,
		"argoproj/argo-cd":                        true,
		"open-policy-agent/opa":                   true,
		"cert-manager/cert-manager":               true,
		"kubernetes-sigs/gateway-api":             true,
		"kubernetes-sigs/controller-runtime":      true,
		"kubernetes-sigs/krew":                    true,
		"prometheus-operator/prometheus-operator": true,
	}
	return coreProjects[fullName]
}

func isKafkaOrMessagingProject(fullName, name string) bool {
	return fullName == "apache/kafka" ||
		fullName == "strimzi/strimzi-kafka-operator" ||
		strings.Contains(name, "kafka") ||
		strings.HasPrefix(fullName, "rabbitmq/") ||
		strings.Contains(name, "rabbitmq")
}

func isMonitoringProject(fullName string) bool {
	monitoringProjects := map[string]bool{
		"prometheus/prometheus":                          true,
		"prometheus/alertmanager":                        true,
		"prometheus/node_exporter":                       true,
		"prometheus/blackbox_exporter":                   true,
		"prometheus-operator/kube-prometheus":            true,
		"grafana/grafana":                                true,
		"grafana/loki":                                   true,
		"grafana/tempo":                                  true,
		"grafana/mimir":                                  true,
		"grafana/k6":                                     true,
		"open-telemetry/opentelemetry-collector":         true,
		"open-telemetry/opentelemetry-collector-contrib": true,
		"open-telemetry/opentelemetry-operator":          true,
		"jaegertracing/jaeger":                           true,
		"thanos-io/thanos":                               true,
		"kubernetes/kube-state-metrics":                  true,
	}
	return monitoringProjects[fullName]
}

func isImportantCNCFProject(fullName string) bool {
	projects := map[string]bool{
		"argoproj/argo-workflows": true,
		"argoproj/argo-events":    true,
		"argoproj/argo-rollouts":  true,
		"vmware-tanzu/velero":     true,
		"rook/rook":               true,
		"crossplane/crossplane":   true,
		"kyverno/kyverno":         true,
		"falcosecurity/falco":     true,
		"aquasecurity/trivy":      true,
		"linkerd/linkerd2":        true,
		"knative/knative":         true,
		"tektoncd/pipeline":       true,
		"dragonflydb/dragonfly":   true,
	}
	return projects[fullName]
}

func (f *IssueFinder) FindIssues(ctx context.Context) ([]Issue, error) {
	var allIssues []Issue
	var mu sync.Mutex
	var wg sync.WaitGroup
	limiter := make(chan struct{}, 5)

	for _, project := range f.projects {
		wg.Add(1)
		go func(p Project) {
			defer wg.Done()

			select {
			case limiter <- struct{}{}:
				defer func() { <-limiter }()
			case <-ctx.Done():
				return
			}

			log.Printf("Checking issues for %s/%s (%d stars)", p.Org, p.Name, p.Stars)

			opts := &github.IssueListByRepoOptions{
				State:     "open",
				Sort:      "created",
				Direction: "desc",
				ListOptions: github.ListOptions{
					PerPage: f.config.MaxIssuesPerRepo,
				},
			}

			issues, _, err := f.client.Issues.ListByRepo(ctx, p.Org, p.Name, opts)
			if err != nil {
				log.Printf("Error fetching issues for %s/%s: %v", p.Org, p.Name, err)
				return
			}

			for _, issue := range issues {
				if ok, reason := f.isCandidateIssue(issue); !ok {
					f.logSkip(p, issue.GetNumber(), reason)
					continue
				}

				issueID := f.issueID(p, issue.GetNumber())

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
					Project:           p,
					Title:             issue.GetTitle(),
					URL:               issue.GetHTMLURL(),
					Number:            issue.GetNumber(),
					Score:             score,
					CreatedAt:         issue.GetCreatedAt().Time,
					UpdatedAt:         issue.GetUpdatedAt().Time,
					Comments:          issue.GetComments(),
					Labels:            labels,
					Language:          "Go",
					IsGoodFirst:       isGoodFirst,
					AssignedToMe:      f.contributionState(issue) == "assigned to you",
					ContributionState: f.contributionState(issue),
					Recommendation:    f.recommendationReason(issue, p),
				}

				mu.Lock()
				allIssues = append(allIssues, newIssue)
				mu.Unlock()
			}
		}(project)
	}

	wg.Wait()

	sort.Slice(allIssues, func(i, j int) bool {
		return allIssues[i].Score > allIssues[j].Score
	})

	allIssues = f.filterLinkedPullRequests(ctx, allIssues)
	if f.config.MaxRecommendations > 0 && len(allIssues) > f.config.MaxRecommendations {
		allIssues = allIssues[:f.config.MaxRecommendations]
	}

	for _, issue := range allIssues {
		issueID := f.issueID(issue.Project, issue.Number)
		if err := f.markIssueSeen(issueID, issue.Project.Name); err != nil {
			log.Printf("Error marking issue %s as seen: %v", issueID, err)
		}

		if err := f.saveIssueHistory(issue); err != nil {
			log.Printf("Error saving issue history: %v", err)
		}
	}

	return allIssues, nil
}

func (f *IssueFinder) filterLinkedPullRequests(ctx context.Context, issues []Issue) []Issue {
	filtered := make([]Issue, 0, len(issues))
	perProjectCounts := make(map[string]int)
	perPriorityCounts := make(map[int]int)
	warnedLinkedPRVerification := false

	for _, issue := range issues {
		if f.outputLimitReached(len(filtered)) {
			break
		}

		if f.projectLimitReached(perProjectCounts, issue.Project) ||
			f.priorityLimitReached(perPriorityCounts, issue.Project.Priority) {
			continue
		}

		if f.config.VerifyLinkedPRs {
			hasPR, err := f.hasOpenLinkedPullRequest(ctx, issue.Project, issue.Number)
			if err != nil {
				if !warnedLinkedPRVerification {
					log.Printf("Warning: linked PR verification failed; skipping unverified issues until GitHub API access recovers: %v", err)
					warnedLinkedPRVerification = true
				}
				continue
			} else if hasPR {
				f.logSkip(issue.Project, issue.Number, "open linked pull request")
				continue
			}
		}

		filtered = append(filtered, issue)
		perProjectCounts[strings.ToLower(issue.Project.FullName())]++
		perPriorityCounts[issue.Project.Priority]++
	}

	return filtered
}

func (f *IssueFinder) outputLimitReached(currentCount int) bool {
	return f.config.MaxRecommendations > 0 && currentCount >= f.config.MaxRecommendations
}

func (f *IssueFinder) projectLimitReached(counts map[string]int, project Project) bool {
	if f.config.MaxPerProject <= 0 {
		return false
	}
	return counts[strings.ToLower(project.FullName())] >= f.config.MaxPerProject
}

func (f *IssueFinder) priorityLimitReached(counts map[int]int, priority int) bool {
	if f.config.MaxPerPriority <= 0 {
		return false
	}
	return counts[priority] >= f.config.MaxPerPriority
}

func (f *IssueFinder) hasOpenLinkedPullRequest(ctx context.Context, project Project, issueNumber int) (bool, error) {
	opts := &github.ListOptions{PerPage: 100}

	for {
		events, resp, err := f.client.Issues.ListIssueTimeline(ctx, project.Org, project.Name, issueNumber, opts)
		if err != nil {
			return false, err
		}

		for _, event := range events {
			if event.GetEvent() != "cross-referenced" {
				continue
			}
			source := event.GetSource()
			if source == nil {
				continue
			}
			sourceIssue := source.GetIssue()
			if sourceIssue == nil || !sourceIssue.IsPullRequest() {
				continue
			}
			if sourceIssue.GetState() == "" || sourceIssue.GetState() == "open" {
				return true, nil
			}
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return false, nil
}

func (f *IssueFinder) SendTelegramAlert(issues []Issue) error {
	if len(issues) == 0 {
		return nil
	}

	if f.bot == nil || f.config.TelegramChatID == 0 {
		return f.outputToConsole(issues)
	}

	return f.sendTelegramMessages(issues)
}

func (f *IssueFinder) outputToConsole(issues []Issue) error {
	header := fmt.Sprintf("🚀 New Learning Opportunities in Go DevOps Projects\n\n")
	log.Print(header)

	limit := f.outputLimit(len(issues))
	for i, issue := range issues {
		if i >= limit {
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
			"%s %s (%.2f)\n%s\n%s/%s (%d★)\nFit: %s\nStatus: %s%s\n\n",
			scoreEmoji,
			truncateString(issue.Title, 80),
			issue.Score,
			issue.URL,
			issue.Project.Org,
			issue.Project.Name,
			issue.Project.Stars,
			issue.Recommendation,
			issue.ContributionState,
			labelsText,
		)

		log.Print(msg)
	}

	return nil
}

func (f *IssueFinder) sendTelegramMessages(issues []Issue) error {
	var messages []string

	header := fmt.Sprintf("🚀 *New Learning Opportunities in Go DevOps Projects*\n\n")
	messages = append(messages, header)

	limit := f.outputLimit(len(issues))
	for i, issue := range issues {
		if i >= limit {
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
			"%s *%s* (%.2f)\n%s\n%s/%s (%d★)\nFit: %s\nStatus: %s%s\n\n",
			scoreEmoji,
			truncateString(issue.Title, 80),
			issue.Score,
			issue.URL,
			issue.Project.Org,
			issue.Project.Name,
			issue.Project.Stars,
			issue.Recommendation,
			issue.ContributionState,
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

func (f *IssueFinder) outputLimit(issueCount int) int {
	if f.config.MaxRecommendations <= 0 || f.config.MaxRecommendations > issueCount {
		return issueCount
	}
	return f.config.MaxRecommendations
}

func (f *IssueFinder) GetTopIssues(limit int) ([]Issue, error) {
	var issues []Issue

	if f.db == nil {
		return issues, nil
	}

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

func main() {
	once := flag.Bool("once", false, "Run a single check and exit")
	maxProjects := flag.Int("max-projects", 20, "Maximum number of projects to check")
	maxIssuesPerRepo := flag.Int("max-issues-per-repo", 30, "Maximum number of issues to fetch per repo")
	maxRecommendations := flag.Int("max-recommendations", 10, "Maximum number of recommendations to output")
	maxPerProject := flag.Int("max-per-project", 2, "Maximum number of recommendations per project")
	maxPerPriority := flag.Int("max-per-priority", 4, "Maximum number of recommendations per priority tier")
	verboseSkips := flag.Bool("verbose-skips", false, "Log every skipped issue with the skip reason")
	verifyLinkedPRs := flag.Bool("verify-linked-prs", true, "Verify and skip issues that already have an open linked PR")
	flag.Parse()

	config := &Config{
		GitHubToken:        os.Getenv("GITHUB_TOKEN"),
		GitHubUsername:     os.Getenv("GITHUB_USERNAME"),
		TelegramBotToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		CheckInterval:      3600,
		MaxIssuesPerRepo:   *maxIssuesPerRepo,
		MaxProjects:        *maxProjects,
		MaxRecommendations: *maxRecommendations,
		MaxPerProject:      *maxPerProject,
		MaxPerPriority:     *maxPerPriority,
		VerboseSkips:       *verboseSkips,
		VerifyLinkedPRs:    *verifyLinkedPRs,
		DBConnectionString: os.Getenv("DB_CONNECTION_STRING"),
		LogDir:             os.Getenv("LOG_DIR"),
	}

	if chatIDStr := os.Getenv("TELEGRAM_CHAT_ID"); chatIDStr != "" {
		var chatID int64
		if _, err := fmt.Sscanf(chatIDStr, "%d", &chatID); err == nil {
			config.TelegramChatID = chatID
		}
	}
	if intervalStr := os.Getenv("CHECK_INTERVAL"); intervalStr != "" {
		var interval int
		if _, err := fmt.Sscanf(intervalStr, "%d", &interval); err == nil {
			config.CheckInterval = interval
		}
	}
	if maxIssuesStr := os.Getenv("MAX_ISSUES_PER_REPO"); maxIssuesStr != "" {
		var maxIssues int
		if _, err := fmt.Sscanf(maxIssuesStr, "%d", &maxIssues); err == nil {
			config.MaxIssuesPerRepo = maxIssues
		}
	}
	if maxRecommendationsStr := os.Getenv("MAX_RECOMMENDATIONS"); maxRecommendationsStr != "" {
		var recommendations int
		if _, err := fmt.Sscanf(maxRecommendationsStr, "%d", &recommendations); err == nil {
			config.MaxRecommendations = recommendations
		}
	}
	if maxPerProjectStr := os.Getenv("MAX_PER_PROJECT"); maxPerProjectStr != "" {
		var maxProjectRecommendations int
		if _, err := fmt.Sscanf(maxPerProjectStr, "%d", &maxProjectRecommendations); err == nil {
			config.MaxPerProject = maxProjectRecommendations
		}
	}
	if maxPerPriorityStr := os.Getenv("MAX_PER_PRIORITY"); maxPerPriorityStr != "" {
		var maxPriorityRecommendations int
		if _, err := fmt.Sscanf(maxPerPriorityStr, "%d", &maxPriorityRecommendations); err == nil {
			config.MaxPerPriority = maxPriorityRecommendations
		}
	}
	if verboseSkipsStr := os.Getenv("VERBOSE_SKIPS"); verboseSkipsStr != "" {
		if parsed, err := strconv.ParseBool(verboseSkipsStr); err == nil {
			config.VerboseSkips = parsed
		}
	}
	if verifyLinkedPRsStr := os.Getenv("VERIFY_LINKED_PRS"); verifyLinkedPRsStr != "" {
		if parsed, err := strconv.ParseBool(verifyLinkedPRsStr); err == nil {
			config.VerifyLinkedPRs = parsed
		}
	}

	if config.CheckInterval == 0 {
		config.CheckInterval = 3600
	}
	if config.MaxIssuesPerRepo == 0 {
		config.MaxIssuesPerRepo = 30
	}
	if config.MaxRecommendations == 0 {
		config.MaxRecommendations = 10
	}
	if config.MaxPerProject == 0 {
		config.MaxPerProject = 2
	}
	if config.MaxPerPriority == 0 {
		config.MaxPerPriority = 4
	}

	if config.LogDir != "" {
		if err := os.MkdirAll(config.LogDir, 0755); err != nil {
			log.Printf("Warning: failed to create log directory: %v", err)
		} else {
			logPath := filepath.Join(config.LogDir, "issues.log")
			file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				log.Printf("Warning: failed to open log file: %v", err)
			} else {
				logFile = file
				log.SetOutput(io.MultiWriter(os.Stdout, file))
			}
		}
	}

	finder, err := NewIssueFinder(config)
	if err != nil {
		log.Fatalf("Failed to create IssueFinder: %v", err)
	}
	defer func() {
		if finder.db != nil {
			finder.db.Close()
		}
		if logFile != nil {
			logFile.Close()
		}
	}()

	finder.projects = finder.projects[:min(config.MaxProjects, len(finder.projects))]

	if *once {
		runOnce(context.Background(), finder)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal, stopping...")
		cancel()
	}()

	log.Println("Starting GitHub Issue Finder...")
	log.Printf("Checking %d projects for good learning issues", len(finder.projects))

	ticker := time.NewTicker(time.Duration(config.CheckInterval) * time.Second)
	defer ticker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				log.Println("Running issue check...")
				issues, err := finder.FindIssues(ctx)
				if err != nil {
					log.Printf("Error finding issues: %v", err)
					continue
				}

				log.Printf("Found %d new issues", len(issues))

				if len(issues) > 0 {
					if err := finder.SendTelegramAlert(issues); err != nil {
						log.Printf("Error sending alert: %v", err)
					} else {
						log.Printf("Successfully output %d issues", len(issues))
					}
				} else {
					log.Println("No new issues found")
				}
			}
		}
	}()

	<-ctx.Done()
	log.Println("Shutdown complete")
}

func runOnce(ctx context.Context, finder *IssueFinder) {
	log.Println("Running single issue check...")
	log.Printf("Checking %d projects for good learning issues", len(finder.projects))

	issues, err := finder.FindIssues(ctx)
	if err != nil {
		log.Printf("Error finding issues: %v", err)
		os.Exit(1)
	}

	log.Printf("Found %d new issues", len(issues))

	if len(issues) == 0 {
		log.Println("No new issues found")
		return
	}

	if err := finder.outputToConsole(issues); err != nil {
		log.Printf("Error outputting issues: %v", err)
		os.Exit(1)
	}
}
