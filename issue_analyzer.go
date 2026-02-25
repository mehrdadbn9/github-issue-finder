package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/v58/github"
)

type IssueAction int

const (
	ActionNone IssueAction = iota
	ActionComment
	ActionRequestAssign
	ActionSkipNeedsTriage
	ActionSkipHasPR
	ActionSkipAssigned
	ActionSkipClosed
	ActionSkipAvoidRepo
)

type IssueAnalysis struct {
	URL              string
	ProjectOwner     string
	ProjectName      string
	IssueNumber      int
	Title            string
	State            string
	HasAssignee      bool
	Assignees        []string
	HasPR            bool
	PRNumber         int
	NeedsTriage      bool
	ReadyForWork     bool
	CanSelfAssign    bool
	BotCanAssign     bool
	Action           IssueAction
	ActionReason     string
	SuggestedBody    string
	Labels           []string
	UserHasCommented bool
	LastActivity     time.Time
}

type IssueAnalyzer struct {
	client     *github.Client
	commentMgr *CommentManager
	username   string
	avoidRepos map[string]bool
}

func NewIssueAnalyzer(client *github.Client, commentMgr *CommentManager, username string) *IssueAnalyzer {
	avoidRepos := map[string]bool{
		"golang/go":         true,
		"grafana/grafana":   true,
		"keycloak/keycloak": true,
		"caddyserver/caddy": true,
	}
	return &IssueAnalyzer{
		client:     client,
		commentMgr: commentMgr,
		username:   username,
		avoidRepos: avoidRepos,
	}
}

func (a *IssueAnalyzer) ShouldSkipRepo(owner, repo string) bool {
	key := fmt.Sprintf("%s/%s", owner, repo)
	return a.avoidRepos[key]
}

func (a *IssueAnalyzer) AnalyzeIssue(ctx context.Context, owner, repo string, number int) (*IssueAnalysis, error) {
	issue, _, err := a.client.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue: %w", err)
	}

	analysis := &IssueAnalysis{
		URL:          issue.GetHTMLURL(),
		ProjectOwner: owner,
		ProjectName:  repo,
		IssueNumber:  number,
		Title:        issue.GetTitle(),
		State:        issue.GetState(),
		HasAssignee:  len(issue.Assignees) > 0,
		Labels:       getLabelNames(issue.Labels),
	}

	for _, u := range issue.Assignees {
		analysis.Assignees = append(analysis.Assignees, u.GetLogin())
	}

	analysis.HasPR = issue.PullRequestLinks != nil

	analysis.NeedsTriage = a.hasTriageLabel(analysis.Labels)

	analysis.UserHasCommented, _ = a.hasUserCommented(ctx, owner, repo, number)

	analysis.HasPR = a.hasLinkedPR(ctx, owner, repo, number)

	analysis.CanSelfAssign = a.canUserSelfAssign(ctx, owner, repo)

	analysis.BotCanAssign = a.canBotAssign(ctx, owner, repo)

	analysis.LastActivity = issue.GetUpdatedAt().Time

	if a.ShouldSkipRepo(owner, repo) {
		analysis.Action = ActionNone
		analysis.ActionReason = fmt.Sprintf("Skipping %s/%s - in avoid list", owner, repo)
	} else {
		analysis.determineAction()
	}

	return analysis, nil
}

func (a *IssueAnalyzer) hasTriageLabel(labels []string) bool {
	triageLabels := []string{
		"needs-triage", "needs triage", "triage", "needs-triaged",
		"status/needs-triage", "awaiting-triage",
	}
	for _, label := range labels {
		for _, triageLabel := range triageLabels {
			if strings.Contains(strings.ToLower(label), strings.ToLower(triageLabel)) {
				return true
			}
		}
	}
	return false
}

func (a *IssueAnalyzer) hasConfirmedLabel(labels []string) bool {
	confirmedLabels := []string{
		"confirmed", "triage/accepted", "triage accepted", "accepted",
		"status/confirmed", "status/accepted", "lifecycle/confirmed",
	}
	for _, label := range labels {
		for _, confirmedLabel := range confirmedLabels {
			if strings.Contains(strings.ToLower(label), strings.ToLower(confirmedLabel)) {
				return true
			}
		}
	}
	return false
}

func (a *IssueAnalyzer) hasGoodFirstIssueLabel(labels []string) bool {
	goodFirstLabels := []string{
		"good first issue", "good-first-issue", "good first issue",
		"first timers only", "first-timers-only", "beginner friendly",
	}
	for _, label := range labels {
		for _, gfiLabel := range goodFirstLabels {
			if strings.Contains(strings.ToLower(label), strings.ToLower(gfiLabel)) {
				return true
			}
		}
	}
	return false
}

type IssueFilterCriteria struct {
	HasGoodFirstIssue bool
	HasConfirmed      bool
	HasAssignee       bool
	HasPR             bool
	IsOpen            bool
}

func (a *IssueAnalyzer) CheckFilterCriteria(labels []string, assignees []*github.User, state string, hasPR bool) IssueFilterCriteria {
	return IssueFilterCriteria{
		HasGoodFirstIssue: a.hasGoodFirstIssueLabel(labels),
		HasConfirmed:      a.hasConfirmedLabel(labels),
		HasAssignee:       len(assignees) > 0,
		HasPR:             hasPR,
		IsOpen:            state == "open",
	}
}

func (a *IssueAnalyzer) MeetsAssignmentCriteria(criteria IssueFilterCriteria) bool {
	return criteria.HasGoodFirstIssue &&
		criteria.HasConfirmed &&
		!criteria.HasAssignee &&
		!criteria.HasPR &&
		criteria.IsOpen
}

func (a *IssueAnalyzer) hasUserCommented(ctx context.Context, owner, repo string, number int) (bool, error) {
	comments, _, err := a.client.Issues.ListComments(ctx, owner, repo, number, nil)
	if err != nil {
		return false, err
	}

	for _, comment := range comments {
		if comment.GetUser().GetLogin() == a.username {
			return true, nil
		}
	}
	return false, nil
}

func (a *IssueAnalyzer) hasLinkedPR(ctx context.Context, owner, repo string, number int) bool {
	query := fmt.Sprintf("repo:%s/%s is:pr %d in:body", owner, repo, number)
	result, _, err := a.client.Search.Issues(ctx, query, nil)
	if err != nil {
		return false
	}
	return *result.Total > 0
}

func (a *IssueAnalyzer) canUserSelfAssign(ctx context.Context, owner, repo string) bool {
	repoInfo, _, err := a.client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return false
	}

	return repoInfo.GetPermissions() != nil &&
		(repoInfo.GetPermissions()["push"] || repoInfo.GetPermissions()["admin"])
}

func (a *IssueAnalyzer) canBotAssign(ctx context.Context, owner, repo string) bool {
	botAssignLabels := []string{
		"bot: assign", "bot-assign", "auto-assign",
		"good first issue", "help wanted", "accepting prs",
	}

	for _, label := range botAssignLabels {
		query := fmt.Sprintf("repo:%s/%s label:\"%s\"", owner, repo, label)
		result, _, err := a.client.Search.Issues(ctx, query, &github.SearchOptions{ListOptions: github.ListOptions{PerPage: 1}})
		if err == nil && *result.Total > 0 {
			return true
		}
	}

	botMentions, _, err := a.client.Search.Issues(ctx,
		fmt.Sprintf("repo:%s/%s /assign in:comments", owner, repo), nil)
	if err == nil && *botMentions.Total > 0 {
		return true
	}

	return false
}

func (a *IssueAnalysis) determineAction() {
	switch {
	case a.State == "closed":
		a.Action = ActionSkipClosed
		a.ActionReason = "Issue is closed"

	case a.HasPR:
		a.Action = ActionSkipHasPR
		a.ActionReason = "Issue has a linked PR"

	case a.HasAssignee:
		a.Action = ActionSkipAssigned
		a.ActionReason = fmt.Sprintf("Already assigned to: %s", strings.Join(a.Assignees, ", "))

	case a.NeedsTriage:
		a.Action = ActionSkipNeedsTriage
		a.ActionReason = "Issue needs triage - wait for maintainer to accept"

	case a.UserHasCommented:
		if a.BotCanAssign {
			a.Action = ActionRequestAssign
			a.ActionReason = "You already commented - request assignment with /assign"
			a.SuggestedBody = "/assign"
		} else {
			a.Action = ActionComment
			a.ActionReason = "You already commented - wait for maintainer response"
			a.SuggestedBody = ""
		}

	default:
		a.Action = ActionComment
		a.ActionReason = "Ready for comment"
		a.SuggestedBody = generateCommentBody(a)
	}

	a.ReadyForWork = a.Action == ActionComment || a.Action == ActionRequestAssign
}

func (a *IssueAnalyzer) ExecuteAction(analysis *IssueAnalysis, ctx context.Context) error {
	switch analysis.Action {
	case ActionComment:
		if analysis.SuggestedBody == "" {
			return fmt.Errorf("no comment body suggested")
		}
		return a.commentMgr.PostComment(ctx, analysis.ProjectOwner, analysis.ProjectName, analysis.IssueNumber, analysis.SuggestedBody)

	case ActionRequestAssign:
		if analysis.BotCanAssign && analysis.SuggestedBody != "" {
			return a.commentMgr.PostComment(ctx, analysis.ProjectOwner, analysis.ProjectName, analysis.IssueNumber, analysis.SuggestedBody)
		}
		return fmt.Errorf("cannot request assignment: %s", analysis.ActionReason)

	default:
		return fmt.Errorf("no action to execute: %s", analysis.ActionReason)
	}
}

func generateCommentBody(analysis *IssueAnalysis) string {
	return GenerateGoodComment(analysis.Title, "", analysis.Labels)
}

func getLabelNames(labels []*github.Label) []string {
	names := make([]string, len(labels))
	for i, label := range labels {
		names[i] = label.GetName()
	}
	return names
}

type BatchAnalyzer struct {
	analyzer *IssueAnalyzer
}

func NewBatchAnalyzer(client *github.Client, commentMgr *CommentManager, username string) *BatchAnalyzer {
	return &BatchAnalyzer{
		analyzer: NewIssueAnalyzer(client, commentMgr, username),
	}
}

func (b *BatchAnalyzer) AnalyzeAndReport(ctx context.Context, issues []struct {
	Owner  string
	Repo   string
	Number int
}) ([]*IssueAnalysis, error) {
	var results []*IssueAnalysis

	for _, issue := range issues {
		analysis, err := b.analyzer.AnalyzeIssue(ctx, issue.Owner, issue.Repo, issue.Number)
		if err != nil {
			log.Printf("Error analyzing %s/%s#%d: %v", issue.Owner, issue.Repo, issue.Number, err)
			continue
		}
		results = append(results, analysis)
	}

	return results, nil
}

func (b *BatchAnalyzer) GetActionableIssues(analyses []*IssueAnalysis) []*IssueAnalysis {
	var actionable []*IssueAnalysis
	for _, a := range analyses {
		if a.ReadyForWork {
			actionable = append(actionable, a)
		}
	}
	return actionable
}

func (b *BatchAnalyzer) GroupByAction(analyses []*IssueAnalysis) map[IssueAction][]*IssueAnalysis {
	groups := make(map[IssueAction][]*IssueAnalysis)
	for _, a := range analyses {
		groups[a.Action] = append(groups[a.Action], a)
	}
	return groups
}

func (b *BatchAnalyzer) PrintReport(analyses []*IssueAnalysis) {
	groups := b.GroupByAction(analyses)

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("ISSUE ANALYSIS REPORT")
	fmt.Println(strings.Repeat("=", 60))

	if issues, ok := groups[ActionComment]; ok && len(issues) > 0 {
		fmt.Printf("\n✅ READY TO COMMENT (%d issues):\n", len(issues))
		for _, a := range issues {
			fmt.Printf("   • %s/%s#%d: %s\n", a.ProjectOwner, a.ProjectName, a.IssueNumber, truncate(a.Title, 50))
		}
	}

	if issues, ok := groups[ActionRequestAssign]; ok && len(issues) > 0 {
		fmt.Printf("\n⏳ READY FOR ASSIGNMENT (%d issues):\n", len(issues))
		for _, a := range issues {
			fmt.Printf("   • %s/%s#%d - Use: %s\n", a.ProjectOwner, a.ProjectName, a.IssueNumber, a.SuggestedBody)
		}
	}

	if issues, ok := groups[ActionSkipNeedsTriage]; ok && len(issues) > 0 {
		fmt.Printf("\n⏸️ WAITING FOR TRIAGE (%d issues):\n", len(issues))
		for _, a := range issues {
			fmt.Printf("   • %s/%s#%d: %s\n", a.ProjectOwner, a.ProjectName, a.IssueNumber, truncate(a.Title, 50))
		}
	}

	if issues, ok := groups[ActionSkipAssigned]; ok && len(issues) > 0 {
		fmt.Printf("\n❌ ALREADY ASSIGNED (%d issues):\n", len(issues))
		for _, a := range issues {
			fmt.Printf("   • %s/%s#%d → %s\n", a.ProjectOwner, a.ProjectName, a.IssueNumber, strings.Join(a.Assignees, ", "))
		}
	}

	if issues, ok := groups[ActionSkipHasPR]; ok && len(issues) > 0 {
		fmt.Printf("\n🔀 HAS PR (%d issues):\n", len(issues))
		for _, a := range issues {
			fmt.Printf("   • %s/%s#%d\n", a.ProjectOwner, a.ProjectName, a.IssueNumber)
		}
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func ParseIssueURL(url string) (owner, repo string, number int, err error) {
	re := regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/issues/(\d+)`)
	matches := re.FindStringSubmatch(url)
	if len(matches) != 4 {
		return "", "", 0, fmt.Errorf("invalid GitHub issue URL: %s", url)
	}
	return matches[1], matches[2], parseNumber(matches[3]), nil
}

func parseNumber(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

func FilterIssues(issues []Issue, filter IssueFilter) []Issue {
	var result []Issue
	for _, issue := range issues {
		if filter.MinScore > 0 && issue.Score < filter.MinScore {
			continue
		}
		if filter.MaxScore > 0 && issue.Score > filter.MaxScore {
			continue
		}
		if filter.MaxComments > 0 && issue.Comments > filter.MaxComments {
			continue
		}
		if filter.MinStars > 0 && issue.Project.Stars < filter.MinStars {
			continue
		}
		if !filter.CreatedAfter.IsZero() && issue.CreatedAt.Before(filter.CreatedAfter) {
			continue
		}
		if !filter.CreatedBefore.IsZero() && issue.CreatedAt.After(filter.CreatedBefore) {
			continue
		}
		if len(filter.Categories) > 0 {
			found := false
			for _, cat := range filter.Categories {
				if issue.Project.Category == cat {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if len(filter.Labels) > 0 {
			found := false
			for _, reqLabel := range filter.Labels {
				for _, issueLabel := range issue.Labels {
					if issueLabel == reqLabel {
						found = true
						break
					}
				}
			}
			if !found {
				continue
			}
		}
		if len(filter.ExcludeLabels) > 0 {
			excluded := false
			for _, exclLabel := range filter.ExcludeLabels {
				for _, issueLabel := range issue.Labels {
					if issueLabel == exclLabel {
						excluded = true
						break
					}
				}
			}
			if excluded {
				continue
			}
		}
		result = append(result, issue)
	}
	return result
}
