package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/go-github/v58/github"
	"github.com/jmoiron/sqlx"
	"golang.org/x/oauth2"
)

type Config struct {
	GitHubToken        string `json:"github_token"`
	TelegramBotToken   string `json:"telegram_bot_token"`
	TelegramChatID     int64  `json:"telegram_chat_id"`
	CheckInterval      int    `json:"check_interval"`
	MaxIssuesPerRepo   int    `json:"max_issues_per_repo"`
	DBConnectionString string `json:"db_connection_string"`
}

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

func NewIssueScorer() *IssueScorer {
	return &IssueScorer{
		weights: map[string]float64{
			"stars_factor":      0.15,
			"comments_factor":   0.20,
			"recency_factor":    0.20,
			"labels_factor":     0.25,
			"difficulty_factor": 0.20,
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

	difficultyScore := s.normalizeDifficulty(issue.Labels, *issue.Body)
	score += difficultyScore * s.weights["difficulty_factor"]

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
	config     *Config
	client     *github.Client
	bot        *tgbotapi.BotAPI
	db         *sqlx.DB
	scorer     *IssueScorer
	projects   []Project
	seenIssues map[string]bool
	mu         sync.RWMutex
}

func NewIssueFinder(config *Config) (*IssueFinder, error) {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: config.GitHubToken},
	)
	tc := oauth2.NewClient(ctx, ts)

	bot, err := tgbotapi.NewBotAPI(config.TelegramBotToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create Telegram bot: %w", err)
	}

	db, err := sqlx.Connect("postgres", config.DBConnectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	finder := &IssueFinder{
		config:     config,
		client:     github.NewClient(tc),
		bot:        bot,
		db:         db,
		scorer:     NewIssueScorer(),
		seenIssues: make(map[string]bool),
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
	}

	sort.Slice(f.projects, func(i, j int) bool {
		return f.projects[i].Stars > f.projects[j].Stars
	})
}

func (f *IssueFinder) FindIssues(ctx context.Context) ([]Issue, error) {
	var allIssues []Issue
	var mu sync.Mutex
	var wg sync.WaitGroup

	issuesChan := make(chan Issue, 100)

	workerCount := 5
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for issue := range issuesChan {
				mu.Lock()
				allIssues = append(allIssues, issue)
				mu.Unlock()
			}
		}()
	}

	for _, project := range f.projects {
		wg.Add(1)
		go func(p Project) {
			defer wg.Done()

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
				if issue.IsPullRequest() {
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

				if err := f.markIssueSeen(issueID, p.Name); err != nil {
					log.Printf("Error marking issue %s as seen: %v", issueID, err)
				}

				if err := f.saveIssueHistory(newIssue); err != nil {
					log.Printf("Error saving issue history: %v", err)
				}
			}
		}(project)
	}

	wg.Wait()
	close(issuesChan)

	sort.Slice(allIssues, func(i, j int) bool {
		return allIssues[i].Score > allIssues[j].Score
	})

	return allIssues, nil
}

func (f *IssueFinder) SendTelegramAlert(issues []Issue) error {
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

func main() {
	config := &Config{
		GitHubToken:        os.Getenv("GITHUB_TOKEN"),
		TelegramBotToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:     683539779,
		CheckInterval:      3600,
		MaxIssuesPerRepo:   10,
		DBConnectionString: "host=localhost user=postgres password=postgres dbname=issue_finder sslmode=disable port=5432",
	}

	if config.GitHubToken == "" {
		log.Fatal("GITHUB_TOKEN environment variable is required")
	}

	if config.TelegramBotToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	finder, err := NewIssueFinder(config)
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
						log.Printf("Error sending Telegram alert: %v", err)
					} else {
						log.Printf("Successfully sent Telegram alert for %d issues", len(issues))
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
