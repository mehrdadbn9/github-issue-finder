package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-github/v58/github"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *MCPServer) RegisterResources(srv *mcp.Server) {
	srv.AddResource(&mcp.Resource{
		URI:         "tracked://",
		Name:        "tracked-issues",
		Description: "All tracked issues in the system",
		MIMEType:    "application/json",
	}, s.handleTrackedIssuesResource)

	srv.AddResource(&mcp.Resource{
		URI:         "config://",
		Name:        "configuration",
		Description: "Current configuration settings",
		MIMEType:    "application/json",
	}, s.handleConfigResource)

	srv.AddResource(&mcp.Resource{
		URI:         "repos://",
		Name:        "configured-repositories",
		Description: "All configured repositories",
		MIMEType:    "application/json",
	}, s.handleReposResource)

	srv.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "issue://{owner}/{repo}/{number}",
		Name:        "issue-details",
		Description: "Get specific issue details",
		MIMEType:    "application/json",
	}, s.handleIssueResource)

	srv.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "repo://{owner}/{repo}",
		Name:        "repo-details",
		Description: "Get repository configuration and stats",
		MIMEType:    "application/json",
	}, s.handleRepoResource)
}

func (s *MCPServer) handleTrackedIssuesResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.tracker == nil {
		return nil, fmt.Errorf("issue tracker not initialized")
	}

	issues, err := s.tracker.GetAll()
	if err != nil {
		return nil, fmt.Errorf("failed to get tracked issues: %w", err)
	}

	result := make([]map[string]any, len(issues))
	for i, issue := range issues {
		result[i] = map[string]any{
			"id":           issue.ID,
			"url":          issue.IssueURL,
			"title":        issue.IssueTitle,
			"project":      fmt.Sprintf("%s/%s", issue.ProjectOrg, issue.ProjectName),
			"issueNumber":  issue.IssueNumber,
			"status":       issue.Status,
			"score":        issue.Score,
			"notes":        issue.Notes,
			"labels":       issue.Labels,
			"hasGoodFirst": issue.HasGoodFirst,
			"hasConfirmed": issue.HasConfirmed,
			"hasAssignee":  issue.HasAssignee,
			"createdAt":    issue.CreatedAt.Format("2006-01-02 15:04:05"),
			"updatedAt":    issue.UpdatedAt.Format("2006-01-02 15:04:05"),
		}
	}

	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tracked issues: %w", err)
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(jsonData),
		}},
	}, nil
}

func (s *MCPServer) handleConfigResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.config == nil {
		return nil, fmt.Errorf("config not initialized")
	}
	config := s.config

	safeConfig := map[string]any{
		"checkInterval":    config.CheckInterval,
		"maxIssuesPerRepo": config.MaxIssuesPerRepo,
		"maxProjects":      config.MaxProjects,
		"logLevel":         config.LogLevel,
		"logFormat":        config.LogFormat,
		"digestMode":       config.DigestMode,
		"digestTime":       config.DigestTime,
		"telegramEnabled":  config.IsTelegramEnabled(),
		"emailEnabled":     config.IsEmailEnabled(),
	}

	if config.Scoring != nil {
		safeConfig["scoring"] = map[string]any{
			"starWeight":               config.Scoring.StarWeight,
			"commentWeight":            config.Scoring.CommentWeight,
			"recencyWeight":            config.Scoring.RecencyWeight,
			"labelWeight":              config.Scoring.LabelWeight,
			"difficultyWeight":         config.Scoring.DifficultyWeight,
			"descriptionQualityWeight": config.Scoring.DescriptionQualityWeight,
			"activityWeight":           config.Scoring.ActivityWeight,
			"maintainerWeight":         config.Scoring.MaintainerWeight,
			"contributorFriendlyBonus": config.Scoring.ContributorFriendlyBonus,
			"maxScore":                 config.Scoring.MaxScore,
		}
	}

	if config.Assignment != nil {
		safeConfig["assignment"] = map[string]any{
			"enabled":      config.Assignment.Enabled,
			"autoMode":     config.Assignment.AutoMode,
			"maxDaily":     config.Assignment.MaxDaily,
			"cooldownMins": config.Assignment.CooldownMins,
		}
	}

	if config.Display != nil {
		safeConfig["display"] = map[string]any{
			"mode":               config.Display.Mode,
			"maxGoodFirstIssues": config.Display.MaxGoodFirstIssues,
			"maxOtherIssues":     config.Display.MaxOtherIssues,
			"showScoreBreakdown": config.Display.ShowScoreBreakdown,
		}
	}

	if config.MCP != nil && config.MCP.Server != nil {
		safeConfig["mcpServer"] = map[string]any{
			"enabled":   config.MCP.Server.Enabled,
			"transport": config.MCP.Server.Transport,
			"httpPort":  config.MCP.Server.HTTPPort,
			"httpHost":  config.MCP.Server.HTTPHost,
		}
	}

	if config.Notification != nil {
		safeConfig["notification"] = map[string]any{
			"localEnabled":     config.Notification.LocalEnabled,
			"emailEnabled":     config.Notification.EmailEnabled,
			"emailMinScore":    config.Notification.EmailMinScore,
			"maxPerHour":       config.Notification.MaxPerHour,
			"maxPerDay":        config.Notification.MaxPerDay,
			"digestMode":       config.Notification.DigestMode,
			"neverNotifyTwice": config.Notification.NeverNotifyTwice,
		}
	}

	jsonData, err := json.MarshalIndent(safeConfig, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(jsonData),
		}},
	}, nil
}

func (s *MCPServer) handleReposResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	repos := s.repoManager.ListRepos()
	categories := s.repoManager.GetCategories()

	result := map[string]any{
		"totalRepos":   len(repos),
		"categories":   categories,
		"repositories": make([]map[string]any, len(repos)),
	}

	repoList := result["repositories"].([]map[string]any)
	for i, repo := range repos {
		repoList[i] = map[string]any{
			"owner":    repo.Owner,
			"name":     repo.Name,
			"fullName": fmt.Sprintf("%s/%s", repo.Owner, repo.Name),
			"category": repo.Category,
			"priority": repo.Priority,
			"minStars": repo.MinStars,
			"language": repo.Language,
			"enabled":  repo.Enabled,
			"labels":   repo.Labels,
		}
	}

	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal repos: %w", err)
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(jsonData),
		}},
	}, nil
}

func (s *MCPServer) handleIssueResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI

	parts := strings.Split(strings.TrimPrefix(uri, "issue://"), "/")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid issue URI format, expected issue://{owner}/{repo}/{number}")
	}

	owner := parts[0]
	repo := parts[1]
	issueNumberStr := parts[2]

	var issueNumber int
	if _, err := fmt.Sscanf(issueNumberStr, "%d", &issueNumber); err != nil {
		return nil, fmt.Errorf("invalid issue number: %s", issueNumberStr)
	}

	issue, _, err := s.client.Issues.Get(ctx, owner, repo, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue: %w", err)
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
		"title":         issue.GetTitle(),
		"url":           issue.GetHTMLURL(),
		"number":        issue.GetNumber(),
		"state":         issue.GetState(),
		"body":          issue.GetBody(),
		"score":         score,
		"author":        issue.GetUser().GetLogin(),
		"labels":        mcpGetLabelNames(issue.Labels),
		"assignees":     assignees,
		"comments":      issue.GetComments(),
		"createdAt":     issue.GetCreatedAt().Format("2006-01-02 15:04:05"),
		"updatedAt":     issue.GetUpdatedAt().Format("2006-01-02 15:04:05"),
		"closedAt":      issue.GetClosedAt().Format("2006-01-02 15:04:05"),
		"isPullRequest": issue.IsPullRequest(),
		"locked":        issue.GetLocked(),
		"project": map[string]any{
			"owner":    owner,
			"name":     repo,
			"stars":    stars,
			"language": language,
		},
		"indicators": map[string]bool{
			"isGoodFirst": hasGoodFirstIssueLabel(issue.Labels),
			"isConfirmed": hasConfirmedLabel(issue.Labels),
			"hasAssignee": len(issue.Assignees) > 0,
		},
	}

	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal issue: %w", err)
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(jsonData),
		}},
	}, nil
}

func (s *MCPServer) handleRepoResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI

	parts := strings.Split(strings.TrimPrefix(uri, "repo://"), "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo URI format, expected repo://{owner}/{repo}")
	}

	owner := parts[0]
	repo := parts[1]

	repoInfo, _, err := s.client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to get repository: %w", err)
	}

	repoConfig := s.repoManager.GetRepo(owner, repo)

	result := map[string]any{
		"owner":         owner,
		"name":          repo,
		"fullName":      fmt.Sprintf("%s/%s", owner, repo),
		"description":   repoInfo.GetDescription(),
		"stars":         repoInfo.GetStargazersCount(),
		"forks":         repoInfo.GetForksCount(),
		"watchers":      repoInfo.GetWatchersCount(),
		"openIssues":    repoInfo.GetOpenIssuesCount(),
		"language":      repoInfo.GetLanguage(),
		"license":       repoInfo.GetLicense().GetName(),
		"homepage":      repoInfo.GetHomepage(),
		"createdAt":     repoInfo.GetCreatedAt().Format("2006-01-02"),
		"updatedAt":     repoInfo.GetUpdatedAt().Format("2006-01-02"),
		"pushedAt":      repoInfo.GetPushedAt().Format("2006-01-02"),
		"isArchived":    repoInfo.GetArchived(),
		"isDisabled":    repoInfo.GetDisabled(),
		"isFork":        repoInfo.GetFork(),
		"hasWiki":       repoInfo.GetHasWiki(),
		"hasIssues":     repoInfo.GetHasIssues(),
		"hasProjects":   repoInfo.GetHasProjects(),
		"hasDownloads":  repoInfo.GetHasDownloads(),
		"defaultBranch": repoInfo.GetDefaultBranch(),
	}

	if repoConfig != nil {
		result["config"] = map[string]any{
			"category": repoConfig.Category,
			"priority": repoConfig.Priority,
			"minStars": repoConfig.MinStars,
			"language": repoConfig.Language,
			"enabled":  repoConfig.Enabled,
			"labels":   repoConfig.Labels,
		}
	} else {
		result["config"] = nil
	}

	issues, _, err := s.client.Issues.ListByRepo(ctx, owner, repo, &github.IssueListByRepoOptions{
		State:  "open",
		Labels: []string{"good first issue"},
		ListOptions: github.ListOptions{
			PerPage: 10,
		},
	})
	if err == nil {
		goodFirstIssues := make([]map[string]any, len(issues))
		for i, issue := range issues {
			goodFirstIssues[i] = map[string]any{
				"number": issue.GetNumber(),
				"title":  issue.GetTitle(),
				"url":    issue.GetHTMLURL(),
				"state":  issue.GetState(),
			}
		}
		result["recentGoodFirstIssues"] = goodFirstIssues
	}

	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal repository: %w", err)
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(jsonData),
		}},
	}, nil
}
