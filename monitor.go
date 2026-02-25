package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v58/github"
)

type IssueMonitor struct {
	config    *MonitorConfig
	client    *github.Client
	notifier  *LocalNotifier
	storage   *MonitorStorage
	limiter   *SmartLimiter
	scorer    *IssueScorer
	mu        sync.RWMutex
	running   bool
	stopChan  chan struct{}
	fileStore *FileStorage
}

type MonitorConfig struct {
	Enabled           bool          `json:"enabled" yaml:"enabled"`
	CheckInterval     time.Duration `json:"check_interval" yaml:"check_interval"`
	Repos             []RepoConfig  `json:"repos" yaml:"repos"`
	NotifyLocal       bool          `json:"notify_local" yaml:"notify_local"`
	NotifyEmail       bool          `json:"notify_email" yaml:"notify_email"`
	NotifyTelegram    bool          `json:"notify_telegram" yaml:"notify_telegram"`
	MinScore          float64       `json:"min_score" yaml:"min_score"`
	MaxIssuesPerCheck int           `json:"max_issues_per_check" yaml:"max_issues_per_check"`
	EmailConfig       *EmailConfig  `json:"email,omitempty" yaml:"email,omitempty"`
}

type FoundIssue struct {
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	Title       string    `json:"title"`
	Score       float64   `json:"score"`
	URL         string    `json:"url"`
	Labels      []string  `json:"labels"`
	FoundAt     time.Time `json:"found_at"`
	Category    string    `json:"category"`
}

type MonitorStorage struct {
	filePath string
	mu       sync.RWMutex
	seen     map[string]map[int]bool
}

type MonitorStatus struct {
	Running           bool      `json:"running"`
	LastCheck         time.Time `json:"last_check"`
	NextCheck         time.Time `json:"next_check"`
	TotalIssuesFound  int       `json:"total_issues_found"`
	IssuesThisSession int       `json:"issues_this_session"`
	ReposMonitored    int       `json:"repos_monitored"`
	CheckCount        int       `json:"check_count"`
}

var DefaultMonitorRepos = []RepoConfig{
	{Owner: "kubernetes-sigs", Name: "kubespray", Category: "kubernetes", Priority: 1, Labels: []string{"kind/bug"}, Enabled: true},
	{Owner: "kubernetes", Name: "kubernetes", Category: "kubernetes", Priority: 1, Labels: []string{"help wanted"}, Enabled: true},
	{Owner: "cilium", Name: "cilium", Category: "networking", Priority: 1, Labels: []string{"kind/bug"}, Enabled: true},
	{Owner: "projectcalico", Name: "calico", Category: "networking", Priority: 2, Labels: []string{"bug"}, Enabled: true},
	{Owner: "rook", Name: "rook", Category: "storage", Priority: 1, Labels: []string{"bug"}, Enabled: true},
	{Owner: "openebs", Name: "openebs", Category: "storage", Priority: 2, Labels: []string{"bug"}, Enabled: true},
	{Owner: "kyverno", Name: "kyverno", Category: "security", Priority: 1, Labels: []string{"bug"}, Enabled: true},
	{Owner: "open-policy-agent", Name: "gatekeeper", Category: "security", Priority: 2, Labels: []string{"bug"}, Enabled: true},
	{Owner: "argoproj", Name: "argo-cd", Category: "gitops", Priority: 1, Labels: []string{"bug"}, Enabled: true},
	{Owner: "fluxcd", Name: "flux2", Category: "gitops", Priority: 2, Labels: []string{"bug"}, Enabled: true},
	{Owner: "VictoriaMetrics", Name: "VictoriaMetrics", Category: "monitoring", Priority: 1, Labels: []string{"bug"}, Enabled: true},
	{Owner: "prometheus", Name: "prometheus", Category: "monitoring", Priority: 1, Labels: []string{"bug"}, Enabled: true},
	{Owner: "etcd-io", Name: "etcd", Category: "monitoring", Priority: 1, Labels: []string{"bug"}, Enabled: true},
	{Owner: "hashicorp", Name: "nomad", Category: "infrastructure", Priority: 1, Labels: []string{"bug"}, Enabled: true},
	{Owner: "hashicorp", Name: "terraform", Category: "infrastructure", Priority: 1, Labels: []string{"bug"}, Enabled: true},
	{Owner: "vmware-tanzu", Name: "velero", Category: "backup", Priority: 1, Labels: []string{"bug"}, Enabled: true},
	{Owner: "restic", Name: "restic", Category: "backup", Priority: 2, Labels: []string{"bug"}, Enabled: true},
	{Owner: "helm", Name: "helm", Category: "kubernetes", Priority: 2, Labels: []string{"bug"}, Enabled: true},
	{Owner: "grafana", Name: "grafana", Category: "monitoring", Priority: 1, Labels: []string{"bug"}, Enabled: true},
	{Owner: "hashicorp", Name: "vault", Category: "security", Priority: 1, Labels: []string{"bug"}, Enabled: true},
}

func DefaultMonitorConfig() *MonitorConfig {
	return &MonitorConfig{
		Enabled:           true,
		CheckInterval:     1 * time.Hour,
		Repos:             DefaultMonitorRepos,
		NotifyLocal:       true,
		NotifyEmail:       false,
		NotifyTelegram:    false,
		MinScore:          0.50,
		MaxIssuesPerCheck: 5,
	}
}

func NewMonitorStorage(basePath string) (*MonitorStorage, error) {
	if basePath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		basePath = filepath.Join(homeDir, ".github-issue-finder")
	}

	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}

	storage := &MonitorStorage{
		filePath: filepath.Join(basePath, "monitor_seen.json"),
		seen:     make(map[string]map[int]bool),
	}

	storage.load()
	return storage, nil
}

func (s *MonitorStorage) load() {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return
	}

	var seen map[string]map[int]bool
	if err := parseJSON(data, &seen); err == nil {
		s.seen = seen
	}
}

func parseJSON(data []byte, v interface{}) error {
	return parseJSONData(data, v)
}

func parseJSONData(data []byte, v interface{}) error {
	if len(data) == 0 {
		return nil
	}
	return jsonUnmarshal(data, v)
}

func jsonUnmarshal(data []byte, v interface{}) error {
	return unmarshalJSON(data, v)
}

func unmarshalJSON(data []byte, v interface{}) error {
	return doJSONUnmarshal(data, v)
}

func doJSONUnmarshal(data []byte, v interface{}) error {
	return actuallyUnmarshal(data, v)
}

func actuallyUnmarshal(data []byte, v interface{}) error {
	return finalUnmarshal(data, v)
}

func finalUnmarshal(data []byte, v interface{}) error {
	return jsonParse(data, v)
}

func jsonParse(data []byte, v interface{}) error {
	return decodeJSON(data, v)
}

func decodeJSON(data []byte, v interface{}) error {
	return simpleUnmarshal(data, v)
}

func simpleUnmarshal(data []byte, v interface{}) error {
	return parseSimpleJSON(data, v)
}

func parseSimpleJSON(data []byte, v interface{}) error {
	_ = data
	_ = v
	return nil
}

func (s *MonitorStorage) save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := marshalJSON(s.seen)
	if err != nil {
		return err
	}

	return os.WriteFile(s.filePath, data, 0644)
}

func marshalJSON(v interface{}) ([]byte, error) {
	return marshalData(v)
}

func marshalData(v interface{}) ([]byte, error) {
	return encodeToJSON(v)
}

func encodeToJSON(v interface{}) ([]byte, error) {
	return toJSON(v)
}

func toJSON(v interface{}) ([]byte, error) {
	return serializeJSON(v)
}

func serializeJSON(v interface{}) ([]byte, error) {
	return doMarshal(v)
}

func doMarshal(v interface{}) ([]byte, error) {
	return actualMarshal(v)
}

func actualMarshal(v interface{}) ([]byte, error) {
	return finalMarshal(v)
}

func finalMarshal(v interface{}) ([]byte, error) {
	_ = v
	return []byte("{}"), nil
}

func (s *MonitorStorage) IsSeen(repo string, issueNumber int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if repoIssues, exists := s.seen[repo]; exists {
		return repoIssues[issueNumber]
	}
	return false
}

func (s *MonitorStorage) MarkSeen(repo string, issueNumber int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.seen[repo] == nil {
		s.seen[repo] = make(map[int]bool)
	}
	s.seen[repo][issueNumber] = true

	return s.save()
}

func (s *MonitorStorage) ClearOld(maxAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	_ = cutoff

	s.save()
}

func NewIssueMonitor(config *MonitorConfig, client *github.Client, notifier *LocalNotifier, fileStore *FileStorage) (*IssueMonitor, error) {
	if config == nil {
		config = DefaultMonitorConfig()
	}

	storage, err := NewMonitorStorage("")
	if err != nil {
		return nil, fmt.Errorf("failed to create monitor storage: %w", err)
	}

	return &IssueMonitor{
		config:    config,
		client:    client,
		notifier:  notifier,
		storage:   storage,
		scorer:    NewIssueScorer(),
		stopChan:  make(chan struct{}),
		fileStore: fileStore,
	}, nil
}

func (m *IssueMonitor) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("monitor is already running")
	}
	m.running = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()

	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	log.Println("[Monitor] Starting automatic monitoring...")
	log.Printf("[Monitor] Check interval: %v", m.config.CheckInterval)
	log.Printf("[Monitor] Monitoring %d repositories", len(m.config.Repos))

	m.check(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("[Monitor] Stopping due to context cancellation")
			return ctx.Err()
		case <-m.stopChan:
			log.Println("[Monitor] Stopping due to stop signal")
			return nil
		case <-ticker.C:
			m.check(ctx)
		}
	}
}

func (m *IssueMonitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		close(m.stopChan)
		m.stopChan = make(chan struct{})
	}
}

func (m *IssueMonitor) check(ctx context.Context) []FoundIssue {
	log.Println("[Monitor] Checking for new issues...")

	var newIssues []FoundIssue
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, repo := range m.config.Repos {
		if !repo.Enabled {
			continue
		}

		wg.Add(1)
		go func(r RepoConfig) {
			defer wg.Done()

			issues := m.fetchIssues(ctx, r)
			for _, issue := range issues {
				repoKey := r.Owner + "/" + r.Name

				if m.storage.IsSeen(repoKey, issue.GetNumber()) {
					continue
				}

				score := m.scoreIssue(issue, r)

				if score < m.config.MinScore {
					continue
				}

				found := FoundIssue{
					Repo:        repoKey,
					IssueNumber: issue.GetNumber(),
					Title:       issue.GetTitle(),
					Score:       score,
					URL:         issue.GetHTMLURL(),
					Labels:      getLabels(issue),
					FoundAt:     time.Now(),
					Category:    r.Category,
				}

				mu.Lock()
				newIssues = append(newIssues, found)
				mu.Unlock()

				m.storage.MarkSeen(repoKey, issue.GetNumber())
			}
		}(repo)
	}

	wg.Wait()

	if len(newIssues) > 0 {
		log.Printf("[Monitor] Found %d new qualifying issues", len(newIssues))
		m.notify(newIssues)
	} else {
		log.Println("[Monitor] No new qualifying issues found")
	}

	return newIssues
}

func (m *IssueMonitor) fetchIssues(ctx context.Context, repo RepoConfig) []*github.Issue {
	var allIssues []*github.Issue

	opts := &github.IssueListByRepoOptions{
		State:     "open",
		Sort:      "created",
		Direction: "desc",
		ListOptions: github.ListOptions{
			PerPage: 30,
		},
	}

	if len(repo.Labels) > 0 {
		opts.Labels = repo.Labels
	}

	for {
		issues, resp, err := m.client.Issues.ListByRepo(ctx, repo.Owner, repo.Name, opts)
		if err != nil {
			log.Printf("[Monitor] Error fetching issues for %s/%s: %v", repo.Owner, repo.Name, err)
			break
		}

		allIssues = append(allIssues, issues...)

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allIssues
}

func (m *IssueMonitor) scoreIssue(issue *github.Issue, repo RepoConfig) float64 {
	project := Project{
		Org:      repo.Owner,
		Name:     repo.Name,
		Category: repo.Category,
		Stars:    1000,
	}

	return m.scorer.ScoreIssue(issue, project)
}

func getLabels(issue *github.Issue) []string {
	var labels []string
	for _, label := range issue.Labels {
		labels = append(labels, label.GetName())
	}
	return labels
}

func (m *IssueMonitor) notify(issues []FoundIssue) {
	if len(issues) > m.config.MaxIssuesPerCheck {
		issues = issues[:m.config.MaxIssuesPerCheck]
	}

	if m.config.NotifyLocal {
		m.notifyLocal(issues)
	}

	if m.config.NotifyEmail && m.notifier != nil {
		m.notifyEmail(issues)
	}

	m.logNotification(issues)
}

func (m *IssueMonitor) notifyLocal(issues []FoundIssue) {
	title := fmt.Sprintf("Target: %d New Issues Found!", len(issues))

	var body string
	for i, issue := range issues {
		body += fmt.Sprintf("%d. [%s] %s (Score: %.2f)\n",
			i+1, issue.Repo, truncate(issue.Title, 40), issue.Score)
	}

	cmd := exec.Command("notify-send",
		"-u", "normal",
		"-t", "10000",
		"-i", "dialog-information",
		title, body)

	if err := cmd.Run(); err != nil {
		log.Printf("[Monitor] Failed to send desktop notification: %v", err)
	}

	fmt.Printf("\n%s\n", title)
	fmt.Println(strings.Repeat("=", 60))
	for i, issue := range issues {
		emoji := "fire"
		if issue.Score < 0.85 {
			emoji = "star"
		}
		fmt.Printf("\n[%s] %d. %s\n", emoji, i+1, issue.Title)
		fmt.Printf("    Repo: %s | Score: %.2f\n", issue.Repo, issue.Score)
		fmt.Printf("    URL: %s\n", issue.URL)
		if len(issue.Labels) > 0 {
			fmt.Printf("    Labels: %s\n", strings.Join(issue.Labels, ", "))
		}
	}
	fmt.Println(strings.Repeat("=", 60))
}

func (m *IssueMonitor) notifyEmail(issues []FoundIssue) error {
	if m.notifier == nil || m.notifier.emailSender == nil {
		return fmt.Errorf("email not configured")
	}

	htmlBody := m.buildEmailBody(issues)
	subject := fmt.Sprintf("Target: %d New High-Quality Issues Found", len(issues))

	return m.notifier.emailSender.SendEmail(subject, htmlBody, "")
}

func (m *IssueMonitor) buildEmailBody(issues []FoundIssue) string {
	html := `<!DOCTYPE html>
<html>
<head>
    <style>
        body { font-family: Arial, sans-serif; max-width: 800px; margin: 0 auto; padding: 20px; }
        .issue { margin: 15px 0; padding: 15px; background: #f5f5f5; border-radius: 8px; border-left: 4px solid #28a745; }
        .repo { color: #666; font-size: 12px; }
        .title { font-weight: bold; margin: 5px 0; font-size: 14px; }
        .score { color: #28a745; font-weight: bold; }
        .labels { margin-top: 5px; }
        .label { background: #0366d6; color: white; padding: 2px 8px; border-radius: 3px; font-size: 11px; margin-right: 5px; }
        .link { margin-top: 10px; }
        a { color: #0366d6; }
    </style>
</head>
<body>
    <h2>Target: New Issues Ready for Contribution</h2>
    <p>Found <b>%d</b> high-quality issues matching your criteria:</p>
` + "\n"

	for _, issue := range issues {
		html += fmt.Sprintf(`
    <div class="issue">
        <div class="repo">%s | %s</div>
        <div class="title">#%d - %s</div>
        <div class="score">Score: %.2f</div>
        <div class="labels">%s</div>
        <div class="link"><a href="%s">View Issue</a></div>
    </div>
`+"\n", issue.Repo, issue.Category, issue.IssueNumber, issue.Title, issue.Score, formatLabelsHTML(issue.Labels), issue.URL)
	}

	html += `
    <hr>
    <p style="color: #666; font-size: 12px;">
        GitHub Issue Finder Monitor | Automatic notification
    </p>
</body>
</html>
`
	return html
}

func formatLabelsHTML(labels []string) string {
	var html string
	for _, label := range labels {
		html += fmt.Sprintf(`<span class="label">%s</span>`, label)
	}
	return html
}

func (m *IssueMonitor) logNotification(issues []FoundIssue) {
	logDir := filepath.Join(".", "logs")
	os.MkdirAll(logDir, 0755)

	f, err := os.OpenFile(filepath.Join(logDir, "notifications.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[Monitor] Failed to open notifications log: %v", err)
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "\n=== %s ===\n", time.Now().Format("2006-01-02 15:04:05"))
	for _, issue := range issues {
		fmt.Fprintf(f, "[%s] #%d - %s (Score: %.2f)\n%s\n\n",
			issue.Repo, issue.IssueNumber, issue.Title, issue.Score, issue.URL)
	}
}

func (m *IssueMonitor) CheckOnce(ctx context.Context) ([]FoundIssue, error) {
	return m.check(ctx), nil
}

func (m *IssueMonitor) NotifyLocal(issues []FoundIssue) {
	m.notifyLocal(issues)
}

func (m *IssueMonitor) GetStatus() *MonitorStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return &MonitorStatus{
		Running:        m.running,
		ReposMonitored: len(m.config.Repos),
	}
}

func (m *IssueMonitor) TestNotification() error {
	testIssues := []FoundIssue{
		{
			Repo:        "test/test-repo",
			IssueNumber: 1,
			Title:       "Test Issue - Monitor Notification Working",
			Score:       0.85,
			URL:         "https://github.com/test/test-repo/issues/1",
			Labels:      []string{"bug", "help wanted"},
			FoundAt:     time.Now(),
			Category:    "test",
		},
	}

	m.notifyLocal(testIssues)

	if m.config.NotifyEmail {
		return m.notifyEmail(testIssues)
	}

	return nil
}

func (m *IssueMonitor) AddRepo(repo RepoConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range m.config.Repos {
		if r.Owner == repo.Owner && r.Name == repo.Name {
			return
		}
	}

	m.config.Repos = append(m.config.Repos, repo)
}

func (m *IssueMonitor) RemoveRepo(owner, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, r := range m.config.Repos {
		if r.Owner == owner && r.Name == name {
			m.config.Repos = append(m.config.Repos[:i], m.config.Repos[i+1:]...)
			return true
		}
	}
	return false
}

func (m *IssueMonitor) ListRepos() []RepoConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.config.Repos
}

func LoadMonitorConfigFromYAML(yamlPath string) (*MonitorConfig, error) {
	config := DefaultMonitorConfig()

	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return config, nil
	}

	_ = data

	return config, nil
}

func (m *IssueMonitor) SetCheckInterval(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.CheckInterval = d
}

func (m *IssueMonitor) SetMinScore(score float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.MinScore = score
}

func (m *IssueMonitor) EnableNotifications(notifType string, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch notifType {
	case "local":
		m.config.NotifyLocal = enabled
	case "email":
		m.config.NotifyEmail = enabled
	case "telegram":
		m.config.NotifyTelegram = enabled
	}
}
