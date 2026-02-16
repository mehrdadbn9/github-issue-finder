package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/go-github/v58/github"
	"github.com/jmoiron/sqlx"
)

type ContributionPolicy struct {
	client           *github.Client
	db               *sqlx.DB
	username         string
	trustedRepos     map[string]*RepoStats
	commentCooldown  time.Duration
	newRepoThreshold int
	openPRRepos      map[string]bool
}

var defaultAvoidRepos = map[string]bool{
	"golang/go":         true,
	"grafana/grafana":   true,
	"keycloak/keycloak": true,
	"caddyserver/caddy": true,
}

var userOpenPRRepos = map[string]bool{
	"VictoriaMetrics/VictoriaMetrics": true,
	"prometheus/alertmanager":         true,
}

type RepoStats struct {
	Owner           string
	Repo            string
	Commits         int
	PRs             int
	IssuesCommented int
	LastActivity    time.Time
	TrustLevel      TrustLevel
}

type TrustLevel int

const (
	TrustNone TrustLevel = iota
	TrustNew
	TrustContributor
	TrustRegular
	TrustMaintainer
)

func (t TrustLevel) String() string {
	switch t {
	case TrustNone:
		return "None"
	case TrustNew:
		return "New"
	case TrustContributor:
		return "Contributor"
	case TrustRegular:
		return "Regular"
	case TrustMaintainer:
		return "Maintainer"
	default:
		return "Unknown"
	}
}

type CommentPolicy struct {
	MinTrustLevel     TrustLevel
	RequireValidation bool
	MaxIssuesPerWeek  int
	RequireLabels     []string
	AvoidLabels       []string
	MinIssueAge       time.Duration
	MaxIssueAge       time.Duration
}

var defaultPolicies = map[TrustLevel]CommentPolicy{
	TrustNone: {
		MinTrustLevel:     TrustNone,
		RequireValidation: true,
		MaxIssuesPerWeek:  0,
		MinIssueAge:       72 * time.Hour,
		AvoidLabels:       []string{"needs-triage", "needs-info", "blocked"},
	},
	TrustNew: {
		MinTrustLevel:     TrustNew,
		RequireValidation: true,
		MaxIssuesPerWeek:  1,
		MinIssueAge:       48 * time.Hour,
		AvoidLabels:       []string{"needs-triage", "needs-info"},
	},
	TrustContributor: {
		MinTrustLevel:     TrustContributor,
		RequireValidation: false,
		MaxIssuesPerWeek:  2,
		MinIssueAge:       24 * time.Hour,
		RequireLabels:     []string{},
	},
	TrustRegular: {
		MinTrustLevel:     TrustRegular,
		RequireValidation: false,
		MaxIssuesPerWeek:  5,
		MinIssueAge:       0,
	},
	TrustMaintainer: {
		MinTrustLevel:     TrustMaintainer,
		RequireValidation: false,
		MaxIssuesPerWeek:  10,
		MinIssueAge:       0,
	},
}

func NewContributionPolicy(client *github.Client, db *sqlx.DB, username string) *ContributionPolicy {
	cp := &ContributionPolicy{
		client:           client,
		db:               db,
		username:         username,
		trustedRepos:     make(map[string]*RepoStats),
		commentCooldown:  24 * time.Hour,
		newRepoThreshold: 1,
	}
	cp.initTables()
	cp.loadTrustLevels()
	return cp
}

func (cp *ContributionPolicy) initTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS repo_trust (
		id SERIAL PRIMARY KEY,
		owner TEXT NOT NULL,
		repo TEXT NOT NULL,
		commits INTEGER DEFAULT 0,
		prs INTEGER DEFAULT 0,
		issues_commented INTEGER DEFAULT 0,
		last_activity TIMESTAMP,
		trust_level INTEGER DEFAULT 0,
		first_contribution TIMESTAMP,
		UNIQUE(owner, repo)
	);

	CREATE TABLE IF NOT EXISTS contribution_history (
		id SERIAL PRIMARY KEY,
		owner TEXT NOT NULL,
		repo TEXT NOT NULL,
		contribution_type TEXT NOT NULL,
		number INTEGER,
		url TEXT,
		created_at TIMESTAMP DEFAULT NOW()
	);
	`
	_, err := cp.db.Exec(schema)
	return err
}

func (cp *ContributionPolicy) loadTrustLevels() {
	var stats []RepoStats
	err := cp.db.Select(&stats, "SELECT owner, repo, commits, prs, issues_commented, last_activity, trust_level FROM repo_trust")
	if err != nil {
		return
	}
	for i := range stats {
		key := fmt.Sprintf("%s/%s", stats[i].Owner, stats[i].Repo)
		cp.trustedRepos[key] = &stats[i]
	}
}

func (cp *ContributionPolicy) FetchUserContributions(ctx context.Context) error {
	log.Printf("Fetching contributions for user %s...", cp.username)

	queries := []string{
		fmt.Sprintf("author:%s is:pr is:merged", cp.username),
		fmt.Sprintf("author:%s is:issue", cp.username),
		fmt.Sprintf("commenter:%s", cp.username),
	}

	for _, query := range queries {
		result, _, err := cp.client.Search.Issues(ctx, query, &github.SearchOptions{
			ListOptions: github.ListOptions{PerPage: 100},
		})
		if err != nil {
			log.Printf("Error fetching contributions: %v", err)
			continue
		}

		for _, item := range result.Issues {
			repo := item.GetRepository()
			if repo == nil {
				continue
			}
			owner := repo.Owner.GetLogin()
			repoName := repo.GetName()

			cp.recordContribution(owner, repoName, item)
		}
	}

	cp.updateTrustLevels()
	return nil
}

func (cp *ContributionPolicy) recordContribution(owner, repo string, item *github.Issue) {
	key := fmt.Sprintf("%s/%s", owner, repo)
	stats, exists := cp.trustedRepos[key]
	if !exists {
		stats = &RepoStats{
			Owner: owner,
			Repo:  repo,
		}
		cp.trustedRepos[key] = stats
	}

	if item.IsPullRequest() {
		stats.PRs++
		if item.GetState() == "closed" {
			stats.Commits++
		}
	} else {
		stats.IssuesCommented++
	}

	if item.GetUpdatedAt().Time.After(stats.LastActivity) {
		stats.LastActivity = item.GetUpdatedAt().Time
	}

	cp.saveRepoStats(stats)
}

func (cp *ContributionPolicy) saveRepoStats(stats *RepoStats) {
	_, err := cp.db.NamedExec(`
		INSERT INTO repo_trust (owner, repo, commits, prs, issues_commented, last_activity, trust_level)
		VALUES (:owner, :repo, :commits, :prs, :issues_commented, :last_activity, :trust_level)
		ON CONFLICT (owner, repo) DO UPDATE SET
			commits = EXCLUDED.commits,
			prs = EXCLUDED.prs,
			issues_commented = EXCLUDED.issues_commented,
			last_activity = EXCLUDED.last_activity,
			trust_level = EXCLUDED.trust_level
	`, stats)
	if err != nil {
		log.Printf("Error saving repo stats: %v", err)
	}
}

func (cp *ContributionPolicy) updateTrustLevels() {
	for _, stats := range cp.trustedRepos {
		stats.TrustLevel = cp.calculateTrustLevel(stats)
		cp.saveRepoStats(stats)
	}
}

func (cp *ContributionPolicy) calculateTrustLevel(stats *RepoStats) TrustLevel {
	totalContrib := stats.Commits + stats.PRs + stats.IssuesCommented

	switch {
	case stats.Commits >= 10 || totalContrib >= 20:
		return TrustMaintainer
	case stats.Commits >= 3 || totalContrib >= 10:
		return TrustRegular
	case stats.Commits >= 1 || stats.PRs >= 1 || totalContrib >= 3:
		return TrustContributor
	case totalContrib >= 1:
		return TrustNew
	default:
		return TrustNone
	}
}

func (cp *ContributionPolicy) GetTrustLevel(owner, repo string) TrustLevel {
	key := fmt.Sprintf("%s/%s", owner, repo)
	if stats, exists := cp.trustedRepos[key]; exists {
		return stats.TrustLevel
	}
	return TrustNone
}

func (cp *ContributionPolicy) CanComment(owner, repo string, issue *github.Issue) (bool, string) {
	trustLevel := cp.GetTrustLevel(owner, repo)
	policy, exists := defaultPolicies[trustLevel]
	if !exists {
		policy = defaultPolicies[TrustNone]
	}

	if policy.MaxIssuesPerWeek == 0 && trustLevel == TrustNone {
		return false, fmt.Sprintf("No contributions to %s/%s. Contribute first before commenting on issues.", owner, repo)
	}

	var commentsThisWeek int
	err := cp.db.Get(&commentsThisWeek, `
		SELECT COUNT(*) FROM contribution_history 
		WHERE owner = $1 AND repo = $2 
		AND contribution_type = 'issue_comment' 
		AND created_at > NOW() - INTERVAL '7 days'
	`, owner, repo)
	if err == nil && commentsThisWeek >= policy.MaxIssuesPerWeek {
		return false, fmt.Sprintf("Weekly comment limit reached for %s/%s (%d/%d)", owner, repo, commentsThisWeek, policy.MaxIssuesPerWeek)
	}

	issueAge := time.Since(issue.GetCreatedAt().Time)
	if issueAge < policy.MinIssueAge {
		return false, fmt.Sprintf("Issue too new (%v old). Wait %v before commenting.", issueAge.Round(time.Hour), policy.MinIssueAge)
	}

	labels := getLabelNames(issue.Labels)
	for _, avoidLabel := range policy.AvoidLabels {
		for _, label := range labels {
			if strings.Contains(strings.ToLower(label), strings.ToLower(avoidLabel)) {
				return false, fmt.Sprintf("Issue has avoid label: %s", avoidLabel)
			}
		}
	}

	if policy.RequireValidation {
		if !cp.isIssueValid(issue) {
			return false, "Issue needs validation - unclear scope or incomplete"
		}
	}

	return true, ""
}

func (cp *ContributionPolicy) isIssueValid(issue *github.Issue) bool {
	body := issue.GetBody()
	if len(body) < 50 {
		return false
	}

	if strings.Contains(strings.ToLower(body), "steps to reproduce") ||
		strings.Contains(body, "```") ||
		strings.Contains(strings.ToLower(body), "expected behavior") {
		return true
	}

	if len(issue.Labels) > 0 {
		for _, label := range issue.Labels {
			name := strings.ToLower(label.GetName())
			if strings.Contains(name, "bug") ||
				strings.Contains(name, "enhancement") ||
				strings.Contains(name, "help wanted") ||
				strings.Contains(name, "good first issue") {
				return true
			}
		}
	}

	return false
}

func (cp *ContributionPolicy) GetRecommendedAction(owner, repo string, issue *github.Issue) string {
	trustLevel := cp.GetTrustLevel(owner, repo)

	switch trustLevel {
	case TrustNone:
		return "CONTRIBUTE_FIRST: Make a PR or meaningful contribution before commenting on issues in this repo"
	case TrustNew:
		return "VALIDATE_FIRST: You're new here. Verify the issue is real and you can solve it before commenting"
	case TrustContributor:
		return "COMMENT_CAREFULLY: You have some history. Make sure your comment adds value"
	case TrustRegular:
		return "PROCEED: You're a regular contributor here"
	case TrustMaintainer:
		return "PROCEED: You have maintainer-level trust"
	}
	return "UNKNOWN"
}

func (cp *ContributionPolicy) GetRepoStats(owner, repo string) *RepoStats {
	key := fmt.Sprintf("%s/%s", owner, repo)
	if stats, exists := cp.trustedRepos[key]; exists {
		return stats
	}
	return &RepoStats{Owner: owner, Repo: repo, TrustLevel: TrustNone}
}

func (cp *ContributionPolicy) ListTrustedRepos() []string {
	var repos []string
	for key, stats := range cp.trustedRepos {
		if stats.TrustLevel >= TrustContributor {
			repos = append(repos, fmt.Sprintf("%s (level: %d, commits: %d)", key, stats.TrustLevel, stats.Commits))
		}
	}
	return repos
}

func (cp *ContributionPolicy) RecordComment(owner, repo string, issueNumber int, url string) error {
	_, err := cp.db.Exec(`
		INSERT INTO contribution_history (owner, repo, contribution_type, number, url)
		VALUES ($1, $2, 'issue_comment', $3, $4)
	`, owner, repo, issueNumber, url)

	key := fmt.Sprintf("%s/%s", owner, repo)
	if stats, exists := cp.trustedRepos[key]; exists {
		stats.IssuesCommented++
		cp.saveRepoStats(stats)
	}

	return err
}
