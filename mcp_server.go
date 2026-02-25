package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/go-github/v58/github"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"golang.org/x/oauth2"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type IssueFinderInterface interface {
	FindIssues(ctx context.Context) ([]Issue, error)
	FindGoodFirstIssues(ctx context.Context, labels []string) ([]Issue, error)
	FindConfirmedGoodFirstIssues(ctx context.Context, category string) ([]ConfirmedGoodFirstIssue, error)
}

type MCPServer struct {
	finder      IssueFinderInterface
	tracker     *IssueTracker
	commentGen  *SmartCommentGenerator
	repoManager *RepoManager
	client      *github.Client
	db          *sqlx.DB
	config      *Config
}

func NewMCPServer() (*MCPServer, error) {
	config, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: config.GitHubToken},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	db, err := sqlx.Connect("postgres", config.DBConnectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	notifier, err := NewLocalNotifier(config.Email)
	if err != nil {
		log.Printf("Warning: failed to create local notifier: %v", err)
	}

	finder, err := NewIssueFinder(config, notifier)
	if err != nil {
		return nil, fmt.Errorf("failed to create issue finder: %w", err)
	}

	tracker, err := NewIssueTracker(db.DB)
	if err != nil {
		log.Printf("Warning: failed to create issue tracker: %v", err)
	}

	commentGen := NewSmartCommentGenerator()

	return &MCPServer{
		finder:      finder,
		tracker:     tracker,
		commentGen:  commentGen,
		repoManager: NewRepoManager(),
		client:      client,
		db:          db,
		config:      config,
	}, nil
}

func (s *MCPServer) CreateServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "github-issue-finder",
		Version: "1.0.0",
	}, nil)

	s.RegisterResources(srv)
	s.RegisterPrompts(srv)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "find_issues",
		Description: "Find issues based on various criteria like score, labels, difficulty, and project",
	}, s.handleFindIssues)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "find_good_first_issues",
		Description: "Find good first issues that are beginner-friendly",
	}, s.handleFindGoodFirstIssues)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "find_confirmed_issues",
		Description: "Find confirmed issues that are ready for assignment",
	}, s.handleFindConfirmedIssues)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_issue_score",
		Description: "Get detailed score breakdown for a specific issue",
	}, s.handleGetIssueScore)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "track_issue",
		Description: "Start tracking an issue for work",
	}, s.handleTrackIssue)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_tracked_issues",
		Description: "List tracked issues, optionally filtered by status",
	}, s.handleListTrackedIssues)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_issue_status",
		Description: "Update the status of a tracked issue",
	}, s.handleUpdateIssueStatus)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "generate_comment",
		Description: "Generate a smart comment for an issue",
	}, s.handleGenerateComment)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_repos",
		Description: "Search configured repositories by name or category",
	}, s.handleSearchRepos)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_stats",
		Description: "Get issue finding statistics",
	}, s.handleGetStats)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_issue_details",
		Description: "Get full details of a specific issue",
	}, s.handleGetIssueDetails)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "analyze_issue",
		Description: "Analyze an issue for resume-worthiness and contribution potential",
	}, s.handleAnalyzeIssue)

	return srv
}

type FindIssuesInput struct {
	Limit      float64  `json:"limit"`
	MinScore   float64  `json:"min_score"`
	Labels     []string `json:"labels"`
	Difficulty string   `json:"difficulty"`
	Project    string   `json:"project"`
}

func (s *MCPServer) handleFindIssues(ctx context.Context, req *mcp.CallToolRequest, args FindIssuesInput) (*mcp.CallToolResult, any, error) {
	if s.finder == nil {
		return nil, nil, fmt.Errorf("issue finder not initialized")
	}

	limit := int(args.Limit)
	if limit <= 0 {
		limit = 20
	}

	minScore := args.MinScore
	if minScore == 0 {
		minScore = 0.5
	}

	labels := args.Labels
	project := args.Project
	difficulty := args.Difficulty

	issues, err := s.finder.FindIssues(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find issues: %w", err)
	}

	var filtered []Issue
	for _, issue := range issues {
		if issue.Score < minScore {
			continue
		}

		if len(labels) > 0 {
			hasLabel := false
			for _, reqLabel := range labels {
				for _, issueLabel := range issue.Labels {
					if strings.Contains(strings.ToLower(issueLabel), strings.ToLower(reqLabel)) {
						hasLabel = true
						break
					}
				}
				if hasLabel {
					break
				}
			}
			if !hasLabel {
				continue
			}
		}

		if project != "" && !strings.Contains(strings.ToLower(issue.Project.Name), strings.ToLower(project)) {
			continue
		}

		if difficulty != "" {
			issueDifficulty := s.assessDifficulty(issue)
			if issueDifficulty != difficulty {
				continue
			}
		}

		filtered = append(filtered, issue)
	}

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	result := make([]map[string]any, len(filtered))
	for i, issue := range filtered {
		result[i] = map[string]any{
			"title":     issue.Title,
			"url":       issue.URL,
			"number":    issue.Number,
			"score":     issue.Score,
			"project":   fmt.Sprintf("%s/%s", issue.Project.Org, issue.Project.Name),
			"stars":     issue.Project.Stars,
			"category":  issue.Project.Category,
			"labels":    issue.Labels,
			"comments":  issue.Comments,
			"createdAt": issue.CreatedAt.Format("2006-01-02"),
		}
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

type FindGoodFirstIssuesInput struct {
	Limit         float64 `json:"limit"`
	MinStars      float64 `json:"min_stars"`
	ConfirmedOnly bool    `json:"confirmed_only"`
}

func (s *MCPServer) handleFindGoodFirstIssues(ctx context.Context, req *mcp.CallToolRequest, args FindGoodFirstIssuesInput) (*mcp.CallToolResult, any, error) {
	if s.finder == nil {
		return nil, nil, fmt.Errorf("issue finder not initialized")
	}

	limit := int(args.Limit)
	if limit <= 0 {
		limit = 20
	}

	minStars := int(args.MinStars)
	if minStars <= 0 {
		minStars = 1000
	}

	confirmedOnly := args.ConfirmedOnly

	var issues []Issue
	var err error

	if confirmedOnly {
		confirmedIssues, err := s.finder.FindConfirmedGoodFirstIssues(ctx, "")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to find confirmed issues: %w", err)
		}
		for _, ci := range confirmedIssues {
			if ci.IsEligible {
				issues = append(issues, ci.Issue)
			}
		}
	} else {
		issues, err = s.finder.FindGoodFirstIssues(ctx, []string{})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to find good first issues: %w", err)
		}
	}

	var filtered []Issue
	for _, issue := range issues {
		if issue.Project.Stars >= minStars {
			filtered = append(filtered, issue)
		}
	}

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	result := make([]map[string]any, len(filtered))
	for i, issue := range filtered {
		result[i] = map[string]any{
			"title":       issue.Title,
			"url":         issue.URL,
			"number":      issue.Number,
			"score":       issue.Score,
			"project":     fmt.Sprintf("%s/%s", issue.Project.Org, issue.Project.Name),
			"stars":       issue.Project.Stars,
			"category":    issue.Project.Category,
			"labels":      issue.Labels,
			"comments":    issue.Comments,
			"createdAt":   issue.CreatedAt.Format("2006-01-02"),
			"isGoodFirst": issue.IsGoodFirst,
		}
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

type FindConfirmedIssuesInput struct {
	Limit    float64 `json:"limit"`
	MinScore float64 `json:"min_score"`
}

func (s *MCPServer) handleFindConfirmedIssues(ctx context.Context, req *mcp.CallToolRequest, args FindConfirmedIssuesInput) (*mcp.CallToolResult, any, error) {
	if s.finder == nil {
		return nil, nil, fmt.Errorf("issue finder not initialized")
	}

	limit := int(args.Limit)
	if limit <= 0 {
		limit = 20
	}

	minScore := args.MinScore
	if minScore == 0 {
		minScore = 0.6
	}

	issues, err := s.finder.FindConfirmedGoodFirstIssues(ctx, "")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find confirmed issues: %w", err)
	}

	var filtered []ConfirmedGoodFirstIssue
	for _, issue := range issues {
		if issue.Score >= minScore && issue.IsEligible {
			filtered = append(filtered, issue)
		}
	}

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	result := make([]map[string]any, len(filtered))
	for i, issue := range filtered {
		result[i] = map[string]any{
			"title":        issue.Title,
			"url":          issue.URL,
			"number":       issue.Number,
			"score":        issue.Score,
			"project":      fmt.Sprintf("%s/%s", issue.Project.Org, issue.Project.Name),
			"stars":        issue.Project.Stars,
			"labels":       issue.Labels,
			"hasConfirmed": issue.HasConfirmedLabel,
			"hasGoodFirst": issue.HasGoodFirstLabel,
			"hasAssignee":  issue.HasAssignee,
			"hasPR":        issue.HasLinkedPR,
			"isEligible":   issue.IsEligible,
			"createdAt":    issue.CreatedAt.Format("2006-01-02"),
		}
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

type GetIssueScoreInput struct {
	Owner       string  `json:"owner"`
	Repo        string  `json:"repo"`
	IssueNumber float64 `json:"issue_number"`
}

func (s *MCPServer) handleGetIssueScore(ctx context.Context, req *mcp.CallToolRequest, args GetIssueScoreInput) (*mcp.CallToolResult, any, error) {
	if s.client == nil {
		return nil, nil, fmt.Errorf("github client not initialized")
	}

	owner := args.Owner
	repo := args.Repo
	issueNumber := int(args.IssueNumber)

	if owner == "" || repo == "" || issueNumber == 0 {
		return nil, nil, fmt.Errorf("owner, repo, and issue_number are required")
	}

	issue, _, err := s.client.Issues.Get(ctx, owner, repo, issueNumber)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get issue: %w", err)
	}

	project := Project{
		Org:  owner,
		Name: repo,
	}

	repoInfo, _, err := s.client.Repositories.Get(ctx, owner, repo)
	if err == nil && repoInfo != nil {
		project.Stars = repoInfo.GetStargazersCount()
		project.Category = repoInfo.GetLanguage()
	}

	scorer := NewIssueScorer()
	score := scorer.ScoreIssue(issue, project)

	result := map[string]any{
		"totalScore":   score,
		"title":        issue.GetTitle(),
		"url":          issue.GetHTMLURL(),
		"number":       issue.GetNumber(),
		"state":        issue.GetState(),
		"comments":     issue.GetComments(),
		"createdAt":    issue.GetCreatedAt().Format("2006-01-02"),
		"hasAssignee":  len(issue.Assignees) > 0,
		"labels":       mcpGetLabelNames(issue.Labels),
		"project":      fmt.Sprintf("%s/%s", owner, repo),
		"projectStars": project.Stars,
		"scoreComponents": map[string]float64{
			"starsWeight":      0.10,
			"commentsWeight":   0.25,
			"recencyWeight":    0.25,
			"labelsWeight":     0.25,
			"difficultyWeight": 0.15,
		},
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

type TrackIssueInput struct {
	Owner       string  `json:"owner"`
	Repo        string  `json:"repo"`
	IssueNumber float64 `json:"issue_number"`
	Notes       string  `json:"notes"`
}

func (s *MCPServer) handleTrackIssue(ctx context.Context, req *mcp.CallToolRequest, args TrackIssueInput) (*mcp.CallToolResult, any, error) {
	if s.tracker == nil {
		return nil, nil, fmt.Errorf("issue tracker not initialized")
	}

	if s.client == nil {
		return nil, nil, fmt.Errorf("github client not initialized")
	}

	owner := args.Owner
	repo := args.Repo
	issueNumber := int(args.IssueNumber)
	notes := args.Notes

	if owner == "" || repo == "" || issueNumber == 0 {
		return nil, nil, fmt.Errorf("owner, repo, and issue_number are required")
	}

	issue, _, err := s.client.Issues.Get(ctx, owner, repo, issueNumber)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get issue: %w", err)
	}

	scorer := NewIssueScorer()
	project := Project{Org: owner, Name: repo}
	score := scorer.ScoreIssue(issue, project)

	trackedIssue := &TrackedIssue{
		IssueURL:     issue.GetHTMLURL(),
		IssueTitle:   issue.GetTitle(),
		ProjectOrg:   owner,
		ProjectName:  repo,
		IssueNumber:  issueNumber,
		Status:       StatusNew,
		Notes:        notes,
		Score:        score,
		Labels:       strings.Join(mcpGetLabelNames(issue.Labels), ","),
		HasGoodFirst: hasGoodFirstIssueLabel(issue.Labels),
		HasConfirmed: hasConfirmedLabel(issue.Labels),
		HasAssignee:  len(issue.Assignees) > 0,
	}

	if err := s.tracker.AddIssue(trackedIssue); err != nil {
		return nil, nil, fmt.Errorf("failed to track issue: %w", err)
	}

	result := map[string]any{
		"success": true,
		"message": "Issue is now being tracked",
		"url":     issue.GetHTMLURL(),
		"title":   issue.GetTitle(),
		"status":  StatusNew,
		"score":   score,
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

type ListTrackedIssuesInput struct {
	Status string `json:"status"`
}

func (s *MCPServer) handleListTrackedIssues(ctx context.Context, req *mcp.CallToolRequest, args ListTrackedIssuesInput) (*mcp.CallToolResult, any, error) {
	if s.tracker == nil {
		return nil, nil, fmt.Errorf("issue tracker not initialized")
	}

	status := args.Status
	if status == "" {
		status = "all"
	}

	var issues []TrackedIssue
	var err error

	if status == "all" {
		issues, err = s.tracker.GetAll()
	} else {
		issues, err = s.tracker.GetByStatus(WorkStatus(status))
	}

	if err != nil {
		return nil, nil, fmt.Errorf("failed to list tracked issues: %w", err)
	}

	result := make([]map[string]any, len(issues))
	for i, issue := range issues {
		result[i] = map[string]any{
			"id":          issue.ID,
			"url":         issue.IssueURL,
			"title":       issue.IssueTitle,
			"project":     fmt.Sprintf("%s/%s", issue.ProjectOrg, issue.ProjectName),
			"issueNumber": issue.IssueNumber,
			"status":      issue.Status,
			"score":       issue.Score,
			"notes":       issue.Notes,
			"labels":      issue.Labels,
			"createdAt":   issue.CreatedAt.Format("2006-01-02"),
		}
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

type UpdateIssueStatusInput struct {
	IssueID   string `json:"issue_id"`
	NewStatus string `json:"new_status"`
}

func (s *MCPServer) handleUpdateIssueStatus(ctx context.Context, req *mcp.CallToolRequest, args UpdateIssueStatusInput) (*mcp.CallToolResult, any, error) {
	if s.tracker == nil {
		return nil, nil, fmt.Errorf("issue tracker not initialized")
	}

	issueID := args.IssueID
	newStatus := args.NewStatus

	if issueID == "" || newStatus == "" {
		return nil, nil, fmt.Errorf("issue_id and new_status are required")
	}

	if err := s.tracker.UpdateStatus(issueID, WorkStatus(newStatus)); err != nil {
		return nil, nil, fmt.Errorf("failed to update status: %w", err)
	}

	result := map[string]any{
		"success":   true,
		"message":   "Issue status updated",
		"issueId":   issueID,
		"newStatus": newStatus,
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

type GenerateCommentInput struct {
	Owner       string  `json:"owner"`
	Repo        string  `json:"repo"`
	IssueNumber float64 `json:"issue_number"`
	CommentType string  `json:"comment_type"`
}

func (s *MCPServer) handleGenerateComment(ctx context.Context, req *mcp.CallToolRequest, args GenerateCommentInput) (*mcp.CallToolResult, any, error) {
	if s.client == nil {
		return nil, nil, fmt.Errorf("github client not initialized")
	}

	owner := args.Owner
	repo := args.Repo
	issueNumber := int(args.IssueNumber)
	commentType := args.CommentType

	if commentType == "" {
		commentType = "interest"
	}

	if owner == "" || repo == "" || issueNumber == 0 {
		return nil, nil, fmt.Errorf("owner, repo, and issue_number are required")
	}

	issue, _, err := s.client.Issues.Get(ctx, owner, repo, issueNumber)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get issue: %w", err)
	}

	details := IssueDetails{
		Title:        issue.GetTitle(),
		Body:         issue.GetBody(),
		Labels:       mcpGetLabelNames(issue.Labels),
		Number:       issueNumber,
		URL:          issue.GetHTMLURL(),
		ProjectOwner: owner,
		ProjectName:  repo,
		Author:       issue.GetUser().GetLogin(),
		CreatedAt:    issue.GetCreatedAt().Time,
		Comments:     issue.GetComments(),
		HasAssignee:  len(issue.Assignees) > 0,
	}

	comment, err := s.commentGen.GenerateSmartComment(details)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate comment: %w", err)
	}

	result := map[string]any{
		"comment":      comment.Body,
		"score":        comment.Score,
		"issueType":    comment.IssueType,
		"qualityFlags": comment.QualityFlags,
		"warnings":     comment.Warnings,
		"issue": map[string]any{
			"title":  issue.GetTitle(),
			"url":    issue.GetHTMLURL(),
			"number": issueNumber,
		},
	}

	if commentType == "assignment" {
		result["suggestedAction"] = "/assign"
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

type SearchReposInput struct {
	Query string  `json:"query"`
	Limit float64 `json:"limit"`
}

func (s *MCPServer) handleSearchRepos(ctx context.Context, req *mcp.CallToolRequest, args SearchReposInput) (*mcp.CallToolResult, any, error) {
	query := args.Query
	limit := int(args.Limit)
	if limit <= 0 {
		limit = 20
	}

	if query == "" {
		return nil, nil, fmt.Errorf("query is required")
	}

	repos := s.repoManager.ListRepos()
	queryLower := strings.ToLower(query)

	var filtered []RepoConfig
	for _, repo := range repos {
		if strings.Contains(strings.ToLower(repo.Name), queryLower) ||
			strings.Contains(strings.ToLower(repo.Owner), queryLower) ||
			strings.Contains(strings.ToLower(repo.Category), queryLower) {
			filtered = append(filtered, repo)
		}
	}

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	result := make([]map[string]any, len(filtered))
	for i, repo := range filtered {
		result[i] = map[string]any{
			"owner":    repo.Owner,
			"name":     repo.Name,
			"category": repo.Category,
			"priority": repo.Priority,
			"minStars": repo.MinStars,
			"language": repo.Language,
			"enabled":  repo.Enabled,
			"labels":   repo.Labels,
		}
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

type GetStatsInput struct {
	Period string `json:"period"`
}

func (s *MCPServer) handleGetStats(ctx context.Context, req *mcp.CallToolRequest, args GetStatsInput) (*mcp.CallToolResult, any, error) {
	period := args.Period
	if period == "" {
		period = "week"
	}

	var since time.Time
	switch period {
	case "day":
		since = time.Now().AddDate(0, 0, -1)
	case "month":
		since = time.Now().AddDate(0, -1, 0)
	default:
		since = time.Now().AddDate(0, 0, -7)
	}

	var totalIssues int
	var avgScore float64
	var categoryCount map[string]int

	if s.db != nil {
		err := s.db.QueryRow(`
			SELECT COUNT(*), COALESCE(AVG(score), 0)
			FROM issue_history
			WHERE created_at >= $1
		`, since).Scan(&totalIssues, &avgScore)
		if err != nil {
			log.Printf("Warning: failed to get stats: %v", err)
		}

		rows, err := s.db.Query(`
			SELECT category, COUNT(*)
			FROM issue_history
			WHERE created_at >= $1
			GROUP BY category
			ORDER BY COUNT(*) DESC
		`, since)
		if err == nil {
			defer rows.Close()
			categoryCount = make(map[string]int)
			for rows.Next() {
				var cat string
				var count int
				if err := rows.Scan(&cat, &count); err == nil {
					categoryCount[cat] = count
				}
			}
		}
	}

	var trackedCount int
	if s.tracker != nil {
		trackedCount, _ = s.tracker.GetActiveCount()
	}

	result := map[string]any{
		"period":          period,
		"since":           since.Format("2006-01-02"),
		"totalIssues":     totalIssues,
		"averageScore":    avgScore,
		"byCategory":      categoryCount,
		"trackedIssues":   trackedCount,
		"configuredRepos": len(s.repoManager.ListRepos()),
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

type GetIssueDetailsInput struct {
	Owner       string  `json:"owner"`
	Repo        string  `json:"repo"`
	IssueNumber float64 `json:"issue_number"`
}

func (s *MCPServer) handleGetIssueDetails(ctx context.Context, req *mcp.CallToolRequest, args GetIssueDetailsInput) (*mcp.CallToolResult, any, error) {
	if s.client == nil {
		return nil, nil, fmt.Errorf("github client not initialized")
	}

	owner := args.Owner
	repo := args.Repo
	issueNumber := int(args.IssueNumber)

	if owner == "" || repo == "" || issueNumber == 0 {
		return nil, nil, fmt.Errorf("owner, repo, and issue_number are required")
	}

	issue, _, err := s.client.Issues.Get(ctx, owner, repo, issueNumber)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get issue: %w", err)
	}

	repoInfo, _, err := s.client.Repositories.Get(ctx, owner, repo)
	var stars int
	var language string
	if err == nil && repoInfo != nil {
		stars = repoInfo.GetStargazersCount()
		language = repoInfo.GetLanguage()
	}

	project := Project{Org: owner, Name: repo, Stars: stars, Category: language}
	scorer := NewIssueScorer()
	score := scorer.ScoreIssue(issue, project)

	var assignees []string
	for _, a := range issue.Assignees {
		assignees = append(assignees, a.GetLogin())
	}

	result := map[string]any{
		"title":     issue.GetTitle(),
		"url":       issue.GetHTMLURL(),
		"number":    issue.GetNumber(),
		"state":     issue.GetState(),
		"body":      issue.GetBody(),
		"score":     score,
		"author":    issue.GetUser().GetLogin(),
		"labels":    mcpGetLabelNames(issue.Labels),
		"assignees": assignees,
		"comments":  issue.GetComments(),
		"createdAt": issue.GetCreatedAt().Format("2006-01-02 15:04:05"),
		"updatedAt": issue.GetUpdatedAt().Format("2006-01-02 15:04:05"),
		"closedAt":  issue.GetClosedAt().Format("2006-01-02 15:04:05"),
		"project": map[string]any{
			"owner":    owner,
			"name":     repo,
			"stars":    stars,
			"language": language,
		},
		"isPullRequest": issue.IsPullRequest(),
		"locked":        issue.GetLocked(),
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

type AnalyzeIssueInput struct {
	Owner       string  `json:"owner"`
	Repo        string  `json:"repo"`
	IssueNumber float64 `json:"issue_number"`
}

func (s *MCPServer) handleAnalyzeIssue(ctx context.Context, req *mcp.CallToolRequest, args AnalyzeIssueInput) (*mcp.CallToolResult, any, error) {
	if s.client == nil {
		return nil, nil, fmt.Errorf("github client not initialized")
	}

	owner := args.Owner
	repo := args.Repo
	issueNumber := int(args.IssueNumber)

	if owner == "" || repo == "" || issueNumber == 0 {
		return nil, nil, fmt.Errorf("owner, repo, and issue_number are required")
	}

	issue, _, err := s.client.Issues.Get(ctx, owner, repo, issueNumber)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get issue: %w", err)
	}

	repoInfo, _, err := s.client.Repositories.Get(ctx, owner, repo)
	var stars int
	var language string
	if err == nil && repoInfo != nil {
		stars = repoInfo.GetStargazersCount()
		language = repoInfo.GetLanguage()
	}

	project := Project{Org: owner, Name: repo, Stars: stars, Category: language}
	scorer := NewIssueScorer()
	score := scorer.ScoreIssue(issue, project)

	labels := mcpGetLabelNames(issue.Labels)
	isGoodFirst := hasGoodFirstIssueLabel(issue.Labels)
	isConfirmed := hasConfirmedLabel(issue.Labels)
	isBug := mcpHasAnyLabelStr(labels, "bug")
	isFeature := mcpHasAnyLabelStr(labels, "feature", "enhancement")

	var reasons []string
	var concerns []string

	if stars >= 10000 {
		reasons = append(reasons, fmt.Sprintf("High-profile project with %d stars - great for resume visibility", stars))
	} else if stars >= 1000 {
		reasons = append(reasons, fmt.Sprintf("Well-known project with %d stars", stars))
	}

	if isGoodFirst {
		reasons = append(reasons, "Marked as good first issue - beginner friendly")
	}
	if isConfirmed {
		reasons = append(reasons, "Confirmed/triaged - maintainers have validated the issue")
	}
	if isBug {
		reasons = append(reasons, "Bug fix - demonstrates problem-solving skills")
	}
	if isFeature {
		reasons = append(reasons, "Feature/enhancement - shows ability to add new functionality")
	}

	if len(issue.Assignees) > 0 {
		concerns = append(concerns, "Already has assignee - may not be available")
	}

	if issue.GetState() == "closed" {
		concerns = append(concerns, "Issue is closed")
	}

	if mcpHasAnyLabelStr(labels, "needs-triage", "needs info", "waiting-for-info") {
		concerns = append(concerns, "Issue needs more information or triage")
	}

	body := strings.ToLower(issue.GetBody())
	if len(body) < 100 {
		concerns = append(concerns, "Issue description is brief - may lack clear requirements")
	}

	if strings.Contains(body, "steps to reproduce") || strings.Contains(body, "```") {
		reasons = append(reasons, "Well-documented with clear reproduction or code examples")
	}

	resumeScore := 0.0
	if stars >= 10000 {
		resumeScore += 0.3
	} else if stars >= 1000 {
		resumeScore += 0.2
	}
	if isBug {
		resumeScore += 0.2
	}
	if isConfirmed {
		resumeScore += 0.15
	}
	if len(issue.Assignees) == 0 {
		resumeScore += 0.15
	}
	if issue.GetComments() <= 5 {
		resumeScore += 0.1
	}
	if len(body) >= 100 {
		resumeScore += 0.1
	}

	if resumeScore > 1.0 {
		resumeScore = 1.0
	}

	result := map[string]any{
		"title":          issue.GetTitle(),
		"url":            issue.GetHTMLURL(),
		"number":         issue.GetNumber(),
		"score":          score,
		"resumeScore":    resumeScore,
		"isResumeWorthy": resumeScore >= 0.5,
		"project": map[string]any{
			"owner":    owner,
			"name":     repo,
			"stars":    stars,
			"language": language,
		},
		"reasons":        reasons,
		"concerns":       concerns,
		"labels":         labels,
		"isGoodFirst":    isGoodFirst,
		"isConfirmed":    isConfirmed,
		"isBug":          isBug,
		"isFeature":      isFeature,
		"hasAssignee":    len(issue.Assignees) > 0,
		"commentCount":   issue.GetComments(),
		"recommendation": mcpGetRecommendation(resumeScore, len(concerns)),
	}

	jsonResult, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(jsonResult)}},
	}, nil, nil
}

func (s *MCPServer) assessDifficulty(issue Issue) string {
	if issue.IsGoodFirst {
		return "easy"
	}

	for _, label := range issue.Labels {
		labelLower := strings.ToLower(label)
		if strings.Contains(labelLower, "good first") || strings.Contains(labelLower, "beginner") || strings.Contains(labelLower, "easy") {
			return "easy"
		}
		if strings.Contains(labelLower, "complex") || strings.Contains(labelLower, "hard") || strings.Contains(labelLower, "difficult") {
			return "hard"
		}
	}

	if issue.Score >= 0.8 {
		return "easy"
	} else if issue.Score >= 0.5 {
		return "medium"
	}

	return "hard"
}

func mcpGetLabelNames(labels []*github.Label) []string {
	names := make([]string, len(labels))
	for i, label := range labels {
		names[i] = label.GetName()
	}
	return names
}

func mcpHasAnyLabelStr(labels []string, targets ...string) bool {
	for _, label := range labels {
		labelLower := strings.ToLower(label)
		for _, target := range targets {
			if strings.Contains(labelLower, strings.ToLower(target)) {
				return true
			}
		}
	}
	return false
}

func mcpGetRecommendation(resumeScore float64, concernCount int) string {
	if resumeScore >= 0.7 && concernCount == 0 {
		return "highly_recommended"
	} else if resumeScore >= 0.5 && concernCount <= 1 {
		return "recommended"
	} else if resumeScore >= 0.3 {
		return "consider"
	}
	return "skip"
}

func RunMCPStdioServer() error {
	mcpServer, err := NewMCPServer()
	if err != nil {
		return fmt.Errorf("failed to create MCP server: %w", err)
	}

	srv := mcpServer.CreateServer()

	return srv.Run(context.Background(), &mcp.StdioTransport{})
}

func RunMCPCommand() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run . mcp [stdio]")
		fmt.Println("  stdio - Run with stdio transport (for CLI usage)")
		os.Exit(1)
	}

	mode := os.Args[1]

	switch mode {
	case "stdio":
		if err := RunMCPStdioServer(); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	default:
		fmt.Printf("Unknown mode: %s\n", mode)
		fmt.Println("Use 'stdio'")
		os.Exit(1)
	}
}
