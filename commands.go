package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v58/github"
)

type CLICommand string

const (
	CmdFind         CLICommand = "find"
	CmdTrack        CLICommand = "track"
	CmdStatus       CLICommand = "status"
	CmdUpdate       CLICommand = "update"
	CmdList         CLICommand = "list"
	CmdStats        CLICommand = "stats"
	CmdDigest       CLICommand = "digest"
	CmdCleanup      CLICommand = "cleanup"
	CmdGoodFirst    CLICommand = "good-first"
	CmdActionable   CLICommand = "actionable"
	CmdConfirmed    CLICommand = "confirmed"
	CmdEmailTest    CLICommand = "email-test"
	CmdBugs         CLICommand = "bugs"
	CmdFeatures     CLICommand = "features"
	CmdNotify       CLICommand = "notify"
	CmdMine         CLICommand = "mine"
	CmdStart        CLICommand = "start"
	CmdSearch       CLICommand = "search"
	CmdComment      CLICommand = "comment"
	CmdConfig       CLICommand = "config"
	CmdEnable       CLICommand = "enable"
	CmdDisable      CLICommand = "disable"
	CmdRepos        CLICommand = "repos"
	CmdHistory      CLICommand = "history"
	CmdPreview      CLICommand = "preview"
	CmdCommit       CLICommand = "commit"
	CmdLimits       CLICommand = "limits"
	CmdMonitor      CLICommand = "monitor"
	CmdMCP          CLICommand = "mcp"
	CmdMCPHTTP      CLICommand = "mcp-http"
	CmdMCPListTools CLICommand = "mcp-list-tools"
	CmdMCPTest      CLICommand = "mcp-test"
)

func ParseCLIArgs() (CLICommand, []string) {
	if len(os.Args) < 2 {
		return CmdFind, nil
	}

	cmd := CLICommand(os.Args[1])
	args := os.Args[2:]

	return cmd, args
}

func RunCLICommand(ctx context.Context, finder *IssueFinder, tracker *IssueTracker, spamManager *NotificationSpamManager, notifier *LocalNotifier, cmd CLICommand, args []string) error {
	switch cmd {
	case CmdFind:
		return runFindCommand(ctx, finder, spamManager)
	case CmdTrack:
		return runTrackCommand(tracker, args)
	case CmdStatus:
		return runStatusCommand(tracker, args)
	case CmdUpdate:
		return runUpdateCommand(tracker, args)
	case CmdList:
		return runListCommand(tracker, args)
	case CmdStats:
		return runStatsCommand(finder, tracker, spamManager, notifier)
	case CmdDigest:
		return runDigestCommand(spamManager, notifier, args)
	case CmdCleanup:
		return runCleanupCommand(finder, spamManager)
	case CmdGoodFirst:
		return runGoodFirstCommand(ctx, finder, spamManager)
	case CmdActionable:
		return runActionableCommand(ctx, finder, spamManager)
	case CmdConfirmed:
		return runConfirmedCommand(ctx, finder, spamManager)
	case CmdEmailTest:
		return runEmailTestCommand(notifier)
	case CmdBugs:
		return runBugsCommand(ctx, finder, spamManager)
	case CmdFeatures:
		return runFeaturesCommand(ctx, finder, spamManager)
	case CmdNotify:
		return runNotifyCommand(ctx, finder, spamManager, notifier, args)
	case CmdMine:
		return runMineCommand(tracker)
	case CmdStart:
		return runStartCommand(ctx, finder)
	case CmdSearch:
		return runSearchCommand(ctx, finder)
	case CmdComment:
		return runCommentCommand(ctx, finder, args)
	case CmdConfig:
		return runConfigCommand(finder, args)
	case CmdEnable:
		return runEnableCommand(finder)
	case CmdDisable:
		return runDisableCommand(finder)
	case CmdRepos:
		return runReposCommand(finder, args)
	case CmdHistory:
		return runHistoryCommand(finder, args)
	case CmdPreview:
		return runPreviewCommand(ctx, finder)
	case CmdCommit:
		return runCommitCommand(ctx, finder)
	case CmdLimits:
		return runLimitsCommand(finder)
	case CmdMCP:
		return runMCPCommand(args)
	case CmdMCPHTTP:
		return runMCPHTTPCommand(args)
	case CmdMCPListTools:
		return runMCPListToolsCommand(args)
	case CmdMCPTest:
		return runMCPTestCommand(args)
	default:
		return fmt.Errorf("unknown command: %s", cmd)
	}
}

func runFindCommand(ctx context.Context, finder *IssueFinder, spamManager *NotificationSpamManager) error {
	fmt.Println("Finding issues...")
	issues, err := finder.FindIssues(ctx)
	if err != nil {
		return err
	}

	filtered := spamManager.FilterNotifications(issues)

	if len(filtered) == 0 {
		fmt.Println("No new issues found after filtering.")
		return nil
	}

	PrintGoodFirstIssues(filtered, "NEW ISSUES FOUND")

	for _, issue := range filtered {
		if err := spamManager.RecordNotification(issue.Project.Name, issue.URL, issue.Number); err != nil {
			fmt.Printf("Warning: failed to record notification for %s: %v\n", issue.URL, err)
		}
	}

	return nil
}

func runTrackCommand(tracker *IssueTracker, args []string) error {
	fs := flag.NewFlagSet("track", flag.ExitOnError)
	url := fs.String("url", "", "Issue URL to track")
	title := fs.String("title", "", "Issue title")
	org := fs.String("org", "", "Project organization")
	repo := fs.String("repo", "", "Project repository")
	number := fs.Int("number", 0, "Issue number")
	status := fs.String("status", "interested", "Initial status (interested, assigned, in_progress)")
	score := fs.Float64("score", 0, "Issue score")
	labels := fs.String("labels", "", "Comma-separated labels")
	notes := fs.String("notes", "", "Notes about the issue")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *url == "" {
		return fmt.Errorf("--url is required")
	}

	issue := &TrackedIssue{
		IssueURL:    *url,
		IssueTitle:  *title,
		ProjectOrg:  *org,
		ProjectName: *repo,
		IssueNumber: *number,
		Status:      WorkStatus(*status),
		Score:       *score,
		Labels:      *labels,
		Notes:       *notes,
	}

	if err := tracker.AddIssue(issue); err != nil {
		return err
	}

	fmt.Printf("✅ Tracking issue: %s\n", *url)
	fmt.Printf("   Status: %s\n", *status)
	return nil
}

func runStatusCommand(tracker *IssueTracker, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	url := fs.String("url", "", "Issue URL to check status")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *url == "" {
		return fmt.Errorf("--url is required")
	}

	issue, err := tracker.GetIssue(*url)
	if err != nil {
		return fmt.Errorf("issue not found: %w", err)
	}

	fmt.Printf("Issue: %s\n", issue.IssueURL)
	fmt.Printf("Title: %s\n", issue.IssueTitle)
	fmt.Printf("Project: %s/%s\n", issue.ProjectOrg, issue.ProjectName)
	fmt.Printf("Status: %s\n", issue.Status)
	fmt.Printf("Score: %.2f\n", issue.Score)
	if issue.Notes != "" {
		fmt.Printf("Notes: %s\n", issue.Notes)
	}
	fmt.Printf("Created: %s\n", issue.CreatedAt.Format("2006-01-02 15:04"))
	fmt.Printf("Updated: %s\n", issue.UpdatedAt.Format("2006-01-02 15:04"))
	if issue.StartedAt != nil {
		fmt.Printf("Started: %s\n", issue.StartedAt.Format("2006-01-02 15:04"))
	}
	if issue.CompletedAt != nil {
		fmt.Printf("Completed: %s\n", issue.CompletedAt.Format("2006-01-02 15:04"))
	}

	return nil
}

func runUpdateCommand(tracker *IssueTracker, args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	url := fs.String("url", "", "Issue URL to update")
	status := fs.String("status", "", "New status (interested, assigned, in_progress, completed, abandoned)")
	notes := fs.String("notes", "", "Update notes")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *url == "" {
		return fmt.Errorf("--url is required")
	}

	if *status != "" {
		if err := tracker.UpdateStatus(*url, WorkStatus(*status)); err != nil {
			return err
		}
		fmt.Printf("✅ Updated status to: %s\n", *status)
	}

	if *notes != "" {
		if err := tracker.UpdateNotes(*url, *notes); err != nil {
			return err
		}
		fmt.Printf("✅ Updated notes\n")
	}

	return nil
}

func runListCommand(tracker *IssueTracker, args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	status := fs.String("status", "", "Filter by status (optional)")
	all := fs.Bool("all", false, "List all tracked issues")

	if err := fs.Parse(args); err != nil {
		return err
	}

	var issues []TrackedIssue
	var err error

	if *status != "" {
		issues, err = tracker.GetByStatus(WorkStatus(*status))
	} else if *all {
		issues, err = tracker.GetAll()
	} else {
		fmt.Println("Use --status <status> or --all to list issues")
		return nil
	}

	if err != nil {
		return err
	}

	if len(issues) == 0 {
		fmt.Println("No tracked issues found.")
		return nil
	}

	fmt.Printf("\nTracked Issues (%d total)\n", len(issues))
	fmt.Println(strings.Repeat("=", 80))

	for _, issue := range issues {
		statusEmoji := getStatusEmoji(issue.Status)
		fmt.Printf("\n%s [%s] %s\n", statusEmoji, issue.Status, issue.IssueTitle)
		fmt.Printf("   Project: %s/%s | Score: %.2f\n", issue.ProjectOrg, issue.ProjectName, issue.Score)
		fmt.Printf("   URL: %s\n", issue.IssueURL)
		if issue.Notes != "" {
			fmt.Printf("   Notes: %s\n", issue.Notes)
		}
	}

	return nil
}

func getStatusEmoji(status WorkStatus) string {
	switch status {
	case StatusInterested:
		return "👀"
	case StatusAssigned:
		return "📌"
	case StatusInProgress:
		return "🔧"
	case StatusCompleted:
		return "✅"
	case StatusAbandoned:
		return "❌"
	default:
		return "❓"
	}
}

func runStatsCommand(finder *IssueFinder, tracker *IssueTracker, spamManager *NotificationSpamManager, notifier *LocalNotifier) error {
	fmt.Println("\n📊 GitHub Issue Finder Statistics")
	fmt.Println(strings.Repeat("=", 80))

	activeCount, err := tracker.GetActiveCount()
	if err == nil {
		fmt.Printf("Active tracked issues: %d\n", activeCount)
	}

	if spamManager != nil {
		stats := spamManager.GetStats()
		fmt.Printf("\nNotification Stats:\n")
		fmt.Printf("  Hourly: %v/%v\n", stats["hourly_notifications"], stats["hourly_limit"])
		fmt.Printf("  Daily: %v/%v\n", stats["daily_notifications"], stats["daily_limit"])
		fmt.Printf("  Recent notifications: %v\n", stats["recent_notifications"])
		fmt.Printf("  Projects notified: %v\n", stats["projects_notified"])
		fmt.Printf("  Daily comments: %v/%v\n", stats["daily_comments"], stats["max_comments_per_day"])
		fmt.Printf("  GitHub calls: %v/%v\n", stats["github_calls"], stats["github_calls_limit"])
	}

	if notifier != nil {
		emailStats := notifier.GetEmailStats()
		fmt.Printf("\nEmail Stats:\n")
		fmt.Printf("  Enabled: %v\n", emailStats["enabled"])
		if emailStats["enabled"] == true {
			fmt.Printf("  Hourly sent: %v/%v\n", emailStats["hourly_sent"], emailStats["hourly_limit"])
			fmt.Printf("  Daily sent: %v/%v\n", emailStats["daily_sent"], emailStats["daily_limit"])
		}
	}

	return nil
}

func runDigestCommand(spamManager *NotificationSpamManager, notifier *LocalNotifier, args []string) error {
	sendEmail := false
	for _, arg := range args {
		if arg == "--send-email" {
			sendEmail = true
		}
	}

	issues, err := spamManager.GetDigestIssues()
	if err != nil {
		return err
	}

	if len(issues) == 0 {
		fmt.Println("No issues in today's digest.")
		return nil
	}

	DisplayIssueDigest(issues, time.Now().Format("2006-01-02"))

	if sendEmail && notifier != nil {
		fmt.Println("\nSending email digest...")
		if err := notifier.SendDigestEmail(issues); err != nil {
			return fmt.Errorf("failed to send email digest: %w", err)
		}
		fmt.Println("Email digest sent successfully!")
	}

	return nil
}

func runConfirmedCommand(ctx context.Context, finder *IssueFinder, spamManager *NotificationSpamManager) error {
	fmt.Println("Finding confirmed good first issues...")
	issues, err := finder.FindConfirmedGoodFirstIssues(ctx, "")
	if err != nil {
		return err
	}

	var eligible []Issue
	var ineligible []Issue

	for _, issue := range issues {
		if issue.IsEligible {
			eligible = append(eligible, issue.Issue)
		} else {
			ineligible = append(ineligible, issue.Issue)
		}
	}

	DisplayEligibleIssues(eligible, ineligible)
	return nil
}

func runEmailTestCommand(notifier *LocalNotifier) error {
	if notifier == nil {
		return fmt.Errorf("notifier not initialized")
	}

	fmt.Println("Testing email configuration...")

	if err := notifier.TestEmail(); err != nil {
		return fmt.Errorf("email test failed: %w", err)
	}

	fmt.Println("✅ Test email sent successfully!")
	return nil
}

func runCleanupCommand(finder *IssueFinder, spamManager *NotificationSpamManager) error {
	fmt.Println("Running cleanup...")

	if err := spamManager.CleanupOldRecords(); err != nil {
		fmt.Printf("Warning: cleanup failed: %v\n", err)
	}

	fmt.Println("✅ Cleanup complete")
	return nil
}

func runGoodFirstCommand(ctx context.Context, finder *IssueFinder, spamManager *NotificationSpamManager) error {
	fmt.Println("Finding good first issues...")
	issues, err := finder.FindGoodFirstIssues(ctx, []string{"Kubernetes", "Monitoring", "CI/CD", "ML/AI"})
	if err != nil {
		return err
	}

	filtered := spamManager.FilterNotifications(issues)
	PrintGoodFirstIssues(filtered, "GOOD FIRST ISSUES")

	return nil
}

func runActionableCommand(ctx context.Context, finder *IssueFinder, spamManager *NotificationSpamManager) error {
	fmt.Println("Finding actionable issues...")
	issues, err := finder.FindActionableIssues(ctx)
	if err != nil {
		return err
	}

	filtered := spamManager.FilterNotifications(issues)
	PrintActionableIssues(filtered)

	return nil
}

func runBugsCommand(ctx context.Context, finder *IssueFinder, spamManager *NotificationSpamManager) error {
	fmt.Println("Finding qualified bug issues...")

	qualifiedFinder := NewQualifiedIssueFinder(finder.client, finder.rateLimiter, finder.projects)
	issues, err := qualifiedFinder.FindBugs(ctx, 0.6)
	if err != nil {
		return err
	}

	filtered := filterQualifiedBySpam(issues, spamManager)
	DisplayQualifiedIssues(filtered, 0)

	return nil
}

func runFeaturesCommand(ctx context.Context, finder *IssueFinder, spamManager *NotificationSpamManager) error {
	fmt.Println("Finding qualified feature issues...")

	qualifiedFinder := NewQualifiedIssueFinder(finder.client, finder.rateLimiter, finder.projects)
	issues, err := qualifiedFinder.FindFeatures(ctx, 0.6)
	if err != nil {
		return err
	}

	filtered := filterQualifiedBySpam(issues, spamManager)
	DisplayQualifiedIssues(filtered, 0)

	return nil
}

func runNotifyCommand(ctx context.Context, finder *IssueFinder, spamManager *NotificationSpamManager, notifier *LocalNotifier, args []string) error {
	sendEmail := false
	sendLocal := true
	minScore := 0.6

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--email":
			sendEmail = true
		case "--local":
			sendLocal = true
		case "--no-local":
			sendLocal = false
		case "--score-min":
			if i+1 < len(args) {
				if val, err := strconv.ParseFloat(args[i+1], 64); err == nil {
					minScore = val
				}
				i++
			}
		}
	}

	fmt.Printf("Finding qualified issues (min score: %.2f)...\n", minScore)

	qualifiedFinder := NewQualifiedIssueFinder(finder.client, finder.rateLimiter, finder.projects)
	issues, err := qualifiedFinder.FindQualifiedIssues(ctx, minScore)
	if err != nil {
		return err
	}

	filtered := filterQualifiedBySpam(issues, spamManager)

	emailCount := 0
	if sendEmail && notifier != nil {
		for _, issue := range filtered {
			if issue.QualifiedScore.TotalScore >= 0.7 {
				if err := notifier.SendQualifiedIssueEmail(issue); err != nil {
					fmt.Printf("Warning: failed to send email for %s: %v\n", issue.URL, err)
				} else {
					emailCount++
				}
			}
		}
	}

	if sendLocal {
		for _, issue := range filtered {
			spamManager.RecordNotification(issue.Project.Name, issue.URL, issue.Number)
		}
	}

	DisplayQualifiedIssues(filtered, emailCount)
	return nil
}

func runMineCommand(tracker *IssueTracker) error {
	fmt.Println("\n📋 YOUR ASSIGNED ISSUES")
	fmt.Println(strings.Repeat("=", 80))

	issues, err := tracker.GetByStatus(StatusInProgress)
	if err != nil {
		return err
	}

	assigned, err := tracker.GetByStatus(StatusAssigned)
	if err == nil {
		issues = append(issues, assigned...)
	}

	if len(issues) == 0 {
		fmt.Println("No issues assigned to you.")
		return nil
	}

	for i, issue := range issues {
		emoji := getStatusEmoji(issue.Status)
		fmt.Printf("\n%s [%d] %s\n", emoji, i+1, issue.IssueTitle)
		fmt.Printf("   Project: %s/%s\n", issue.ProjectOrg, issue.ProjectName)
		fmt.Printf("   Status: %s | Score: %.2f\n", issue.Status, issue.Score)
		fmt.Printf("   URL: %s\n", issue.IssueURL)
		if issue.Notes != "" {
			fmt.Printf("   Notes: %s\n", issue.Notes)
		}
	}

	fmt.Printf("\nTotal: %d issues\n", len(issues))
	return nil
}

func filterQualifiedBySpam(issues []QualifiedIssue, spamManager *NotificationSpamManager) []QualifiedIssue {
	var filtered []QualifiedIssue
	for _, issue := range issues {
		canNotify, _ := spamManager.CanNotify(issue.Project.Name, issue.URL)
		if canNotify {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

func PrintUsage() {
	fmt.Println("GitHub Issue Finder - Find Qualified Issues")
	fmt.Println()
	fmt.Println("Usage: github-issue-finder <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  start              Start automated daily search")
	fmt.Println("  search             One-time search")
	fmt.Println("  preview            Preview what would be commented (dry-run)")
	fmt.Println("  commit             Actually post comments")
	fmt.Println("  limits             Show current smart limits status")
	fmt.Println("  comment <issue>    Comment on specific issue")
	fmt.Println("  status             Show today's status")
	fmt.Println("  config             Configure settings")
	fmt.Println("  enable             Enable auto mode")
	fmt.Println("  disable            Disable auto mode")
	fmt.Println("  repos              List managed repos")
	fmt.Println("  repos add <owner/repo>     Add repo")
	fmt.Println("  repos remove <owner/repo>  Remove repo")
	fmt.Println("  history            Show comment history")
	fmt.Println("  find               Find qualified issues (default)")
	fmt.Println("  bugs               Find qualified bug issues")
	fmt.Println("  features           Find qualified feature issues")
	fmt.Println("  notify             Find and send notifications for qualified issues")
	fmt.Println("  mine               Check your assigned issues")
	fmt.Println("  stats              Show statistics")
	fmt.Println("  digest             Show daily digest of issues")
	fmt.Println("  track              Track an issue you're working on")
	fmt.Println("  update             Update a tracked issue's status or notes")
	fmt.Println("  list               List tracked issues")
	fmt.Println("  email-test         Test email configuration")
	fmt.Println("  cleanup            Clean up old notification records")
	fmt.Println()
	fmt.Println("Monitor Commands:")
	fmt.Println("  monitor start      Start continuous monitoring daemon")
	fmt.Println("  monitor stop       Stop monitoring daemon")
	fmt.Println("  monitor status     Show monitor status and configuration")
	fmt.Println("  monitor check      Run a single monitoring check now")
	fmt.Println("  monitor notify     Test notification system")
	fmt.Println()
	fmt.Println("MCP Server Commands:")
	fmt.Println("  mcp                Run as MCP server (stdio mode for Claude Desktop, etc.)")
	fmt.Println("  mcp-http           Run as MCP HTTP server (for web integrations)")
	fmt.Println("  mcp-list-tools     List all available MCP tools")
	fmt.Println("  mcp-test           Test MCP server functionality")
	fmt.Println()
	fmt.Println("Notify Options:")
	fmt.Println("  --email          Send email for high-scoring issues (>0.7)")
	fmt.Println("  --local          Send local/desktop notifications (default)")
	fmt.Println("  --no-local       Disable local notifications")
	fmt.Println("  --score-min N    Minimum score threshold (default: 0.6)")
	fmt.Println()
	fmt.Println("Smart Limits Configuration:")
	fmt.Println("  Base daily limit: 3 comments")
	fmt.Println("  Max daily limit: 7 comments (with high-quality issues)")
	fmt.Println("  Weekly cap: 15 comments")
	fmt.Println("  Max per repo per day: 1 comment")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  github-issue-finder preview     # See what would be commented")
	fmt.Println("  github-issue-finder commit      # Post the comments")
	fmt.Println("  github-issue-finder limits      # Check current limits")
	fmt.Println("  github-issue-finder start")
	fmt.Println("  github-issue-finder search")
	fmt.Println("  github-issue-finder comment https://github.com/owner/repo/issues/123")
	fmt.Println("  github-issue-finder repos add kubernetes/kubernetes")
	fmt.Println("  github-issue-finder find")
	fmt.Println("  github-issue-finder bugs")
	fmt.Println("  github-issue-finder notify --email --score-min 0.7")
	fmt.Println("  github-issue-finder digest --send-email")
	fmt.Println("  github-issue-finder mine")
	fmt.Println("  github-issue-finder stats")
}

func runStartCommand(ctx context.Context, finder *IssueFinder) error {
	if finder.autoFinder == nil {
		return fmt.Errorf("auto finder not initialized")
	}

	fmt.Println("Starting automated daily search...")
	if err := finder.autoFinder.Enable(); err != nil {
		return err
	}

	return finder.autoFinder.Run(ctx)
}

func runSearchCommand(ctx context.Context, finder *IssueFinder) error {
	if finder.autoFinder == nil {
		return runFindCommand(ctx, finder, finder.antiSpam)
	}

	fmt.Println("Searching for issues...")
	issues, err := finder.autoFinder.Search(ctx)
	if err != nil {
		return err
	}

	if len(issues) == 0 {
		fmt.Println("No qualifying issues found.")
		return nil
	}

	fmt.Printf("\n🔍 TOP ISSUES FOUND (%d total)\n", len(issues))
	fmt.Println(strings.Repeat("=", 80))

	for i, issue := range issues {
		if i >= 20 {
			break
		}

		grade := issue.Score.Grade
		emoji := "🔥"
		if grade == "B" || grade == "B+" {
			emoji = "⭐"
		} else if grade == "C" || grade == "D" {
			emoji = "✨"
		}

		fmt.Printf("\n%s [%s] %s\n", emoji, grade, issue.IssueData.Title)
		fmt.Printf("   Score: %.2f | %s/%s\n", issue.Score.Total, issue.Project.Org, issue.Project.Name)
		fmt.Printf("   Category: %s | Comments: %d\n", issue.Project.Category, issue.IssueData.Comments)
		fmt.Printf("   URL: %s\n", issue.IssueData.URL)
		if len(issue.IssueData.Labels) > 0 {
			fmt.Printf("   Labels: %s\n", strings.Join(issue.IssueData.Labels, ", "))
		}
		fmt.Println(strings.Repeat("-", 80))
	}

	return nil
}

func runCommentCommand(ctx context.Context, finder *IssueFinder, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: comment <issue-url>")
	}

	issueURL := args[0]
	org, repo, number, err := ParseIssueNumberFromURL(issueURL)
	if err != nil {
		return fmt.Errorf("invalid issue URL: %w", err)
	}

	fmt.Printf("Posting comment on %s/%s#%d...\n", org, repo, number)

	commentBody := "Hi! I'd like to help with this issue. I'll start working on it and provide updates."
	if len(args) > 1 {
		commentBody = strings.Join(args[1:], " ")
	}

	_, _, err = finder.client.Issues.CreateComment(ctx, org, repo, number, &github.IssueComment{
		Body: &commentBody,
	})
	if err != nil {
		return fmt.Errorf("failed to post comment: %w", err)
	}

	fmt.Println("✅ Comment posted successfully")
	return nil
}

func runConfigCommand(finder *IssueFinder, args []string) error {
	if finder.autoFinder == nil {
		return fmt.Errorf("auto finder not initialized")
	}

	status, err := finder.autoFinder.GetStatus()
	if err != nil {
		return err
	}

	fmt.Println("\n⚙️  AUTO FINDER CONFIGURATION")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Enabled: %v\n", status.Enabled)
	fmt.Printf("Auto Comment: %v\n", status.AutoComment)
	fmt.Printf("Comments Today: %d/%d\n", status.CommentsToday, status.MaxCommentsPerDay)
	fmt.Printf("Min Score to Comment: %.2f\n", status.MinScoreToComment)
	fmt.Printf("Found Issues Today: %d\n", status.FoundIssuesCount)

	return nil
}

func runEnableCommand(finder *IssueFinder) error {
	if finder.autoFinder == nil {
		return fmt.Errorf("auto finder not initialized")
	}

	if err := finder.autoFinder.Enable(); err != nil {
		return err
	}

	fmt.Println("✅ Auto finder enabled")
	return nil
}

func runDisableCommand(finder *IssueFinder) error {
	if finder.autoFinder == nil {
		return fmt.Errorf("auto finder not initialized")
	}

	if err := finder.autoFinder.Disable(); err != nil {
		return err
	}

	fmt.Println("❌ Auto finder disabled")
	return nil
}

func runReposCommand(finder *IssueFinder, args []string) error {
	if finder.repoManager == nil {
		return fmt.Errorf("repo manager not initialized")
	}

	if len(args) >= 2 {
		action := args[0]
		repoPath := args[1]

		parts := strings.Split(repoPath, "/")
		if len(parts) != 2 {
			return fmt.Errorf("invalid repo format, use owner/repo")
		}

		switch action {
		case "add":
			repo := RepoConfig{
				Owner:   parts[0],
				Name:    parts[1],
				Enabled: true,
			}
			finder.repoManager.AddRepo(repo)
			fmt.Printf("✅ Added %s to managed repos\n", repoPath)
			return nil
		case "remove":
			if finder.repoManager.RemoveRepo(parts[0], parts[1]) {
				fmt.Printf("✅ Removed %s from managed repos\n", repoPath)
			} else {
				fmt.Printf("⚠️  %s not found in managed repos\n", repoPath)
			}
			return nil
		}
	}

	repos := finder.repoManager.ListRepos()
	categories := finder.repoManager.GetCategories()

	fmt.Println("\n📁 MANAGED REPOSITORIES")
	fmt.Println(strings.Repeat("=", 80))

	for _, cat := range categories {
		catRepos := finder.repoManager.ListByCategory(cat)
		if len(catRepos) == 0 {
			continue
		}

		fmt.Printf("\n%s (%d repos)\n", strings.ToTitle(cat), len(catRepos))
		fmt.Println(strings.Repeat("-", 40))

		for _, repo := range catRepos {
			enabled := "✓"
			if !repo.Enabled {
				enabled = "✗"
			}
			fmt.Printf("  [%s] %s/%s (Priority: %d, Stars: %d+)\n",
				enabled, repo.Owner, repo.Name, repo.Priority, repo.MinStars)
		}
	}

	fmt.Printf("\nTotal: %d repositories\n", len(repos))
	return nil
}

func runHistoryCommand(finder *IssueFinder, args []string) error {
	if finder.autoFinder == nil {
		return fmt.Errorf("auto finder not initialized")
	}

	limit := 20
	if len(args) > 0 {
		if val, err := strconv.Atoi(args[0]); err == nil && val > 0 {
			limit = val
		}
	}

	records, err := finder.autoFinder.GetHistory(limit)
	if err != nil {
		return err
	}

	if len(records) == 0 {
		fmt.Println("No comment history found.")
		return nil
	}

	fmt.Printf("\n📜 COMMENT HISTORY (last %d)\n", len(records))
	fmt.Println(strings.Repeat("=", 80))

	for i, record := range records {
		fmt.Printf("\n[%d] %s\n", i+1, record.IssueURL)
		fmt.Printf("    Repo: %s#%d\n", record.Repo, record.IssueNumber)
		fmt.Printf("    Commented: %s\n", record.CommentedAt.Format("2006-01-02 15:04"))
		fmt.Printf("    Score: %.2f\n", record.Score)
		if record.CommentText != "" {
			preview := record.CommentText
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			fmt.Printf("    Preview: %s\n", preview)
		}
	}

	return nil
}

func runPreviewCommand(ctx context.Context, finder *IssueFinder) error {
	if finder.autoFinder == nil {
		return fmt.Errorf("auto finder not initialized")
	}

	fmt.Println("\n👁️  PREVIEW MODE - Dry Run")
	fmt.Println(strings.Repeat("=", 80))

	previews, err := finder.autoFinder.Preview(ctx)
	if err != nil {
		return err
	}

	if len(previews) == 0 {
		fmt.Println("No issues selected for commenting.")
		return nil
	}

	fmt.Printf("\nWould comment on %d issue(s):\n", len(previews))
	for i, preview := range previews {
		fmt.Printf("\n[%d] %s/%s#%d\n", i+1, preview.Repo, preview.Repo, preview.IssueNumber)
		fmt.Printf("    Title: %s\n", preview.Title)
		fmt.Printf("    Score: %.2f\n", preview.Score)
		fmt.Printf("    URL: %s\n", preview.URL)
		fmt.Printf("    Comment Preview:\n")
		commentPreview := preview.Comment
		if len(commentPreview) > 150 {
			commentPreview = commentPreview[:150] + "..."
		}
		fmt.Printf("    %s\n", strings.ReplaceAll(commentPreview, "\n", "\n    "))
	}

	fmt.Println("\n💡 Run 'github-issue-finder commit' to post these comments")
	return nil
}

func runCommitCommand(ctx context.Context, finder *IssueFinder) error {
	if finder.autoFinder == nil {
		return fmt.Errorf("auto finder not initialized")
	}

	fmt.Println("\n✍️  COMMITTING COMMENTS")
	fmt.Println(strings.Repeat("=", 80))

	previews, err := finder.autoFinder.Preview(ctx)
	if err != nil {
		return err
	}

	if len(previews) == 0 {
		fmt.Println("No issues selected for commenting.")
		return nil
	}

	fmt.Printf("Ready to post %d comment(s).\n", len(previews))
	fmt.Print("Proceed? (y/N): ")

	var response string
	fmt.Scanln(&response)
	if strings.ToLower(response) != "y" {
		fmt.Println("Aborted.")
		return nil
	}

	results, err := finder.autoFinder.CommitComments(ctx)
	if err != nil {
		return err
	}

	successCount := 0
	for _, result := range results {
		if result.Success {
			successCount++
			fmt.Printf("✅ %s/%s#%d - Comment posted\n", result.Repo, result.Repo, result.IssueNumber)
		} else {
			fmt.Printf("❌ %s/%s#%d - Failed: %s\n", result.Repo, result.Repo, result.IssueNumber, result.Error)
		}
	}

	fmt.Printf("\n📊 Summary: %d/%d comments posted successfully\n", successCount, len(results))
	return nil
}

func runLimitsCommand(finder *IssueFinder) error {
	if finder == nil || finder.autoFinder == nil {
		fmt.Println("\n📊 SMART COMMENTING LIMITS")
		fmt.Println(strings.Repeat("=", 80))
		fmt.Println("\nSmart limiter not initialized (requires database connection).")
		fmt.Println("\nDefault configuration:")
		fmt.Println("   Base daily limit: 3")
		fmt.Println("   Max daily limit: 7 (with high-quality issues)")
		fmt.Println("   Weekly cap: 15")
		fmt.Println("   Max per repo per day: 1")
		fmt.Println("   Quality threshold: 0.85")
		fmt.Println("   Min score to comment: 0.70")
		return nil
	}

	if finder.autoFinder.smartLimiter == nil {
		fmt.Println("\n📊 SMART COMMENTING LIMITS")
		fmt.Println(strings.Repeat("=", 80))
		fmt.Println("\nSmart limiter not available.")
		return nil
	}

	status := finder.autoFinder.smartLimiter.GetStatus()

	fmt.Println("\n📊 SMART COMMENTING LIMITS")
	fmt.Println(strings.Repeat("=", 80))

	fmt.Printf("\n📅 Daily Limits:\n")
	fmt.Printf("   Comments today: %d\n", status.TodayComments)
	fmt.Printf("   Repos commented: %d\n", status.TodayRepos)
	fmt.Printf("   Base limit: %d\n", status.BaseLimit)
	fmt.Printf("   Max limit (high quality): %d\n", status.MaxLimit)
	fmt.Printf("   Remaining today: %d\n", status.RemainingToday)

	fmt.Printf("\n📆 Weekly Limits:\n")
	fmt.Printf("   Comments this week: %d\n", status.WeekComments)
	fmt.Printf("   Weekly cap: %d\n", status.WeeklyLimit)
	fmt.Printf("   Remaining this week: %d\n", status.RemainingWeekly)

	fmt.Printf("\n🎯 Quality Thresholds:\n")
	fmt.Printf("   Quality threshold (great): %.2f\n", status.QualityThreshold)
	fmt.Printf("   Minimum score to comment: %.2f\n", status.MinScore)

	if len(status.ReposCommented) > 0 {
		fmt.Printf("\n📝 Repos commented today:\n")
		for repo := range status.ReposCommented {
			fmt.Printf("   - %s\n", repo)
		}
	}

	fmt.Println("\n💡 Tips:")
	fmt.Println("   - Base limit (3) is used for normal quality issues")
	fmt.Println("   - Max limit (7) applies when you have 3+ high-quality issues from different repos")
	fmt.Println("   - Each repo gets at most 1 comment per day")
	fmt.Println("   - Weekly cap prevents over-commenting across the week")

	return nil
}

func ParseIssueNumberFromURL(url string) (string, string, int, error) {
	parts := strings.Split(url, "/")
	if len(parts) < 7 {
		return "", "", 0, fmt.Errorf("invalid GitHub URL format")
	}

	org := parts[3]
	repo := parts[4]
	numberStr := parts[6]

	number, err := strconv.Atoi(numberStr)
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid issue number: %w", err)
	}

	return org, repo, number, nil
}

func runMonitorCommand(ctx context.Context, finder *IssueFinder, args []string) error {
	if len(args) == 0 {
		PrintMonitorUsage()
		return nil
	}

	subCmd := args[0]
	switch subCmd {
	case "start":
		return runStartMonitorCommand(ctx, finder)
	case "stop":
		return runStopMonitorCommand(finder)
	case "status":
		return runMonitorStatusCommand(finder)
	case "check":
		return runMonitorCheckCommand(ctx, finder)
	case "notify":
		return runMonitorNotifyCommand(finder)
	default:
		return fmt.Errorf("unknown monitor subcommand: %s", subCmd)
	}
}

func runStartMonitorCommand(ctx context.Context, finder *IssueFinder) error {
	if finder == nil {
		return fmt.Errorf("finder not initialized")
	}

	monitorConfig := DefaultMonitorConfig()
	monitor, err := NewIssueMonitor(monitorConfig, finder.client, finder.notifier, finder.fileStore)
	if err != nil {
		return fmt.Errorf("failed to create monitor: %w", err)
	}

	fmt.Println("\n🔍 Starting Issue Monitor...")
	fmt.Printf("   Check interval: %v\n", monitorConfig.CheckInterval)
	fmt.Printf("   Repositories: %d\n", len(monitorConfig.Repos))
	fmt.Printf("   Min score: %.2f\n", monitorConfig.MinScore)
	fmt.Printf("   Notifications: Local=%v, Email=%v\n", monitorConfig.NotifyLocal, monitorConfig.NotifyEmail)
	fmt.Println("\nPress Ctrl+C to stop...")

	return monitor.Start(ctx)
}

func runStopMonitorCommand(finder *IssueFinder) error {
	fmt.Println("Monitor stop signal sent (no persistent monitor running)")
	return nil
}

func runMonitorStatusCommand(finder *IssueFinder) error {
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

func runMonitorCheckCommand(ctx context.Context, finder *IssueFinder) error {
	if finder == nil {
		return fmt.Errorf("finder not initialized")
	}

	fmt.Println("\n🔍 Running One-Time Monitor Check...")
	fmt.Println(strings.Repeat("=", 60))

	monitorConfig := DefaultMonitorConfig()
	monitor, err := NewIssueMonitor(monitorConfig, finder.client, finder.notifier, finder.fileStore)
	if err != nil {
		return fmt.Errorf("failed to create monitor: %w", err)
	}

	issues, err := monitor.CheckOnce(ctx)
	if err != nil {
		return fmt.Errorf("check failed: %w", err)
	}

	if len(issues) == 0 {
		fmt.Println("\n✅ Check complete. No new qualifying issues found.")
	} else {
		fmt.Printf("\n✅ Check complete. Found %d new qualifying issues.\n", len(issues))
	}

	return nil
}

func runMonitorNotifyCommand(finder *IssueFinder) error {
	if finder == nil {
		return fmt.Errorf("finder not initialized")
	}

	fmt.Println("\n🔔 Testing Monitor Notifications...")
	fmt.Println(strings.Repeat("=", 60))

	monitorConfig := DefaultMonitorConfig()
	monitor, err := NewIssueMonitor(monitorConfig, finder.client, finder.notifier, finder.fileStore)
	if err != nil {
		return fmt.Errorf("failed to create monitor: %w", err)
	}

	if err := monitor.TestNotification(); err != nil {
		return fmt.Errorf("notification test failed: %w", err)
	}

	fmt.Println("\n✅ Test notification sent successfully!")
	return nil
}

func PrintMonitorUsage() {
	fmt.Println("\n📊 MONITOR COMMANDS")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("\nUsage: github-issue-finder monitor <subcommand>")
	fmt.Println("\nSubcommands:")
	fmt.Println("  start     Start the monitoring daemon (runs continuously)")
	fmt.Println("  stop      Stop the monitoring daemon")
	fmt.Println("  status    Show monitor status and configuration")
	fmt.Println("  check     Run a single check now (one-time)")
	fmt.Println("  notify    Test notification system")
	fmt.Println("\nExamples:")
	fmt.Println("  ./github-issue-finder monitor start")
	fmt.Println("  ./github-issue-finder monitor check")
	fmt.Println("  ./github-issue-finder monitor status")
	fmt.Println("  ./github-issue-finder monitor notify")
}

func runMCPCommand(args []string) error {
	fmt.Println("\n🔌 Starting MCP Server (stdio mode)...")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("This mode is for use with Claude Desktop and other MCP clients.")
	fmt.Println("The server communicates via stdio (standard input/output).")
	fmt.Println("\nPress Ctrl+C to stop.")
	fmt.Println(strings.Repeat("-", 60))

	if err := RunMCPStdioServer(); err != nil {
		return fmt.Errorf("MCP server error: %w", err)
	}
	return nil
}

func runMCPHTTPCommand(args []string) error {
	port := 8080
	for i := 0; i < len(args); i++ {
		if args[i] == "--port" && i+1 < len(args) {
			if p, err := strconv.Atoi(args[i+1]); err == nil && p > 0 {
				port = p
			}
			i++
		}
	}

	fmt.Printf("\n🔌 Starting MCP HTTP Server on port %d...\n", port)
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("This mode is for web integrations and HTTP-based MCP clients.")
	fmt.Printf("Server will be available at: http://localhost:%d/mcp\n", port)
	fmt.Println("\nPress Ctrl+C to stop.")
	fmt.Println(strings.Repeat("-", 60))

	return RunMCPHTTPServer(port)
}

func runMCPListToolsCommand(args []string) error {
	fmt.Println("\n📋 Available MCP Tools")
	fmt.Println(strings.Repeat("=", 60))

	tools := []struct {
		name        string
		description string
	}{
		{"find_issues", "Find issues based on various criteria like score, labels, difficulty, and project"},
		{"find_good_first_issues", "Find good first issues that are beginner-friendly"},
		{"find_confirmed_issues", "Find confirmed issues that are ready for assignment"},
		{"get_issue_score", "Get detailed score breakdown for a specific issue"},
		{"track_issue", "Start tracking an issue for work"},
		{"list_tracked_issues", "List tracked issues, optionally filtered by status"},
		{"update_issue_status", "Update the status of a tracked issue"},
		{"generate_comment", "Generate a smart comment for an issue"},
		{"search_repos", "Search configured repositories by name or category"},
		{"get_stats", "Get issue finding statistics"},
		{"get_issue_details", "Get full details of a specific issue"},
		{"analyze_issue", "Analyze an issue for resume-worthiness and contribution potential"},
	}

	for i, tool := range tools {
		fmt.Printf("\n%d. %s\n", i+1, tool.name)
		fmt.Printf("   %s\n", tool.description)
	}

	fmt.Printf("\n\nTotal: %d tools available\n", len(tools))
	fmt.Println("\n💡 Usage: Use these tools through an MCP client like Claude Desktop")
	return nil
}

func runMCPTestCommand(args []string) error {
	fmt.Println("\n🧪 Testing MCP Server Functionality...")
	fmt.Println(strings.Repeat("=", 60))

	fmt.Println("\n1. Checking MCP server initialization...")
	server, err := NewMCPServer()
	if err != nil {
		return fmt.Errorf("failed to create MCP server: %w", err)
	}
	fmt.Println("   ✅ MCP server initialized successfully")

	fmt.Println("\n2. Checking tool registration...")
	srv := server.CreateServer()
	if srv == nil {
		return fmt.Errorf("failed to create MCP server instance")
	}
	fmt.Println("   ✅ MCP tools registered successfully")

	fmt.Println("\n3. Testing GitHub API connection...")
	ctx := context.Background()
	_, _, err = server.client.Users.Get(ctx, "")
	if err != nil {
		fmt.Printf("   ⚠️  GitHub API connection issue: %v\n", err)
	} else {
		fmt.Println("   ✅ GitHub API connection successful")
	}

	fmt.Println("\n4. Testing database connection...")
	var testResult int
	err = server.db.Get(&testResult, "SELECT 1")
	if err != nil {
		fmt.Printf("   ⚠️  Database connection issue: %v\n", err)
	} else {
		fmt.Println("   ✅ Database connection successful")
	}

	fmt.Println("\n✅ MCP server test complete!")
	fmt.Println("\nTo start the server:")
	fmt.Println("  ./github-issue-finder mcp          # For stdio mode (Claude Desktop)")
	fmt.Println("  ./github-issue-finder mcp-http     # For HTTP mode (web integrations)")

	return nil
}
