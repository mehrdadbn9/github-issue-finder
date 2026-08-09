package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v58/github"
)

type QualifiedIssueType string

const (
	QualifiedTypeBug        QualifiedIssueType = "bug"
	QualifiedTypeFeature    QualifiedIssueType = "feature"
	QualifiedTypeEnhance    QualifiedIssueType = "enhancement"
	QualifiedTypeHelpWanted QualifiedIssueType = "help-wanted"
)

type QualifiedIssue struct {
	Issue
	Type               QualifiedIssueType
	IsConfirmed        bool
	IsApproved         bool
	HasClearRepro      bool
	HasAcceptanceCrit  bool
	Priority           string
	MaintainerApproved bool
	WhyGood            []string
	QualifiedScore     QualifiedIssueScore
}

type QualifiedIssueScore struct {
	ProjectStars          float64
	IsBug                 float64
	HasClearReproduction  float64
	MaintainerApproved    float64
	HasAcceptanceCriteria float64
	Priority              float64
	NoAssignee            float64
	NoOpenPR              float64
	LowComments           float64
	GoodDescription       float64
	HasCodeSnippets       float64
	RecentActivity        float64
	TotalScore            float64
}

type QualifiedIssueFinder struct {
	client      *github.Client
	rateLimiter *RateLimiter
	scorer      *QualifiedScorer
	filter      *QualifiedIssueFilter
	projects    []Project
	seenIssues  map[string]bool
	mu          sync.RWMutex
}

type QualifiedScorer struct {
	config *QualifiedScoringConfig
}

type QualifiedScoringConfig struct {
	ImpactWeight        float64
	ApprovalWeight      float64
	AccessibilityWeight float64
	QualityWeight       float64
	MinScore            float64
}

type QualifiedIssueFilter struct {
	IncludeTypes      []QualifiedIssueType
	IncludeLabels     []string
	ExcludeLabels     []string
	MinStars          int
	RequireApproval   bool
	RequireNoAssignee bool
	RequireClearScope bool
}

func DefaultQualifiedScoringConfig() *QualifiedScoringConfig {
	return &QualifiedScoringConfig{
		ImpactWeight:        0.40,
		ApprovalWeight:      0.30,
		AccessibilityWeight: 0.20,
		QualityWeight:       0.10,
		MinScore:            0.60,
	}
}

func NewQualifiedScorer(config *QualifiedScoringConfig) *QualifiedScorer {
	if config == nil {
		config = DefaultQualifiedScoringConfig()
	}
	return &QualifiedScorer{config: config}
}

func NewQualifiedIssueFinder(client *github.Client, rateLimiter *RateLimiter, projects []Project) *QualifiedIssueFinder {
	return &QualifiedIssueFinder{
		client:      client,
		rateLimiter: rateLimiter,
		scorer:      NewQualifiedScorer(nil),
		filter:      DefaultQualifiedIssueFilter(),
		projects:    projects,
		seenIssues:  make(map[string]bool),
	}
}

func DefaultQualifiedIssueFilter() *QualifiedIssueFilter {
	return &QualifiedIssueFilter{
		IncludeTypes: []QualifiedIssueType{
			QualifiedTypeBug,
			QualifiedTypeFeature,
			QualifiedTypeEnhance,
			QualifiedTypeHelpWanted,
		},
		IncludeLabels: []string{
			"confirmed", "triage/accepted", "accepted", "approved",
			"help wanted", "priority/P1", "priority/P2", "priority/P3",
		},
		ExcludeLabels: []string{
			"question", "support", "wontfix", "duplicate", "invalid",
			"stale", "needs-triage", "waiting-for-info",
		},
		MinStars:          100,
		RequireNoAssignee: true,
	}
}

func (f *QualifiedIssueFinder) FindQualifiedIssues(ctx context.Context, minScore float64) ([]QualifiedIssue, error) {
	var allIssues []QualifiedIssue
	var mu sync.Mutex
	var wg sync.WaitGroup

	issuesChan := make(chan QualifiedIssue, 100)

	collectorDone := make(chan struct{})
	go func() {
		for issue := range issuesChan {
			mu.Lock()
			allIssues = append(allIssues, issue)
			mu.Unlock()
		}
		close(collectorDone)
	}()

	maxProjects := 50
	projectsToCheck := f.projects
	if len(projectsToCheck) > maxProjects {
		projectsToCheck = projectsToCheck[:maxProjects]
	}

	batchSize := 10
	for i := 0; i < len(projectsToCheck); i += batchSize {
		end := i + batchSize
		if end > len(projectsToCheck) {
			end = len(projectsToCheck)
		}

		for j := i; j < end; j++ {
			project := projectsToCheck[j]
			wg.Add(1)
			go func(p Project) {
				defer wg.Done()
				issues := f.findQualifiedIssuesForProject(ctx, p, minScore)
				for _, issue := range issues {
					issuesChan <- issue
				}
			}(project)
		}
		wg.Wait()
		time.Sleep(500 * time.Millisecond)
	}

	close(issuesChan)
	<-collectorDone

	sort.Slice(allIssues, func(i, j int) bool {
		return allIssues[i].QualifiedScore.TotalScore > allIssues[j].QualifiedScore.TotalScore
	})

	return allIssues, nil
}

func (f *QualifiedIssueFinder) findQualifiedIssuesForProject(ctx context.Context, project Project, minScore float64) []QualifiedIssue {
	var qualifiedIssues []QualifiedIssue

	opts := &github.IssueListByRepoOptions{
		State:     "open",
		Sort:      "created",
		Direction: "desc",
		ListOptions: github.ListOptions{
			PerPage: 30,
		},
	}

	var issues []*github.Issue
	var err error

	err = f.rateLimiter.executeWithRetry(ctx, fmt.Sprintf("qualified issues %s/%s", project.Org, project.Name), func() (*github.Response, error) {
		var apiErr error
		issues, _, apiErr = f.client.Issues.ListByRepo(ctx, project.Org, project.Name, opts)
		return nil, apiErr
	})

	if err != nil {
		log.Printf("Error fetching issues for %s/%s: %v", project.Org, project.Name, err)
		return qualifiedIssues
	}

	for _, issue := range issues {
		if issue.IsPullRequest() {
			continue
		}

		qualified := f.evaluateIssue(issue, project)
		if qualified == nil || qualified.QualifiedScore.TotalScore < minScore {
			continue
		}

		// Availability (hard gate): a linked PR — open, or recently closed
		// unmerged — means the issue is already being worked or is contested.
		// Only checked for issues that already passed the cheap gates and the
		// score threshold, so the extra timeline API call runs on survivors only.
		if blocked, reason := f.linkedPRBlocksAvailability(ctx, project, issue.GetNumber()); blocked {
			log.Printf("skipping %s/%s#%d (not available): %s", project.Org, project.Name, issue.GetNumber(), reason)
			continue
		}

		qualifiedIssues = append(qualifiedIssues, *qualified)
	}

	return qualifiedIssues
}

// staleAttemptWindow bounds how recently a closed, unmerged linked PR still
// signals a contested issue. Older closed attempts are treated as water under the
// bridge and do not block availability.
const staleAttemptWindow = 60 * 24 * time.Hour

// linkedPRBlocksAvailability reports whether a timeline-linked PR means the issue
// is not cleanly available to claim, and why. Two blocking signals, both drawn
// from a single timeline call:
//   - an OPEN linked PR — someone is actively working it;
//   - a recently CLOSED, unmerged linked PR — a stalled or contested attempt a
//     human should review before re-claiming (may be reopened, blocked, or
//     superseded by a duplicate; e.g. kedacore/keda#7691, whose only direct link
//     #7713 closed unmerged and whose canonical fix #7700 is only reachable two
//     hops away and so never surfaces here).
//
// Merged PRs and old closed attempts (> staleAttemptWindow) do not block. On API
// error we fail open — better to surface a maybe-taken issue than silently drop a
// good one on a flaky call. This replaces the old scoreNoOpenPR signal, which
// inspected issue.PullRequestLinks (nil for a normal issue — it only says whether
// the issue itself is a PR, never whether a PR is linked to it).
func (f *QualifiedIssueFinder) linkedPRBlocksAvailability(ctx context.Context, project Project, number int) (bool, string) {
	return collectLinkedPRs(ctx, project, number,
		func(cb func() (*github.Response, error)) error {
			return f.rateLimiter.executeWithRetry(ctx, fmt.Sprintf("timeline %s/%s#%d", project.Org, project.Name, number), cb)
		},
		f.client,
		func(n int) *github.PullRequest { return f.getPR(ctx, project, n) },
	)
}

// collectLinkedPRs reads an issue's timeline and returns the availability
// verdict. Shared by both finders: the qualified path uses it during its own
// gating, and the Telegram alert path uses it right before sending, which is
// the only place that actually protects the user from a wasted PR.
//
// Collect the linked PRs (I/O), then hand the pure decision to
// linkedPRVerdict. The timeline's typed Issue does not expose merge status, so
// closed linked PRs need one light PR fetch to fill Merged/ClosedAt (rare path).
func collectLinkedPRs(
	ctx context.Context,
	project Project,
	number int,
	withRetry func(func() (*github.Response, error)) error,
	client *github.Client,
	getPR func(int) *github.PullRequest,
) (bool, string) {
	var events []*github.Timeline
	err := withRetry(func() (*github.Response, error) {
		var apiErr error
		events, _, apiErr = client.Issues.ListIssueTimeline(ctx, project.Org, project.Name, number, &github.ListOptions{PerPage: 100})
		return nil, apiErr
	})
	if err != nil {
		log.Printf("timeline check failed for %s/%s#%d: %v", project.Org, project.Name, number, err)
		return false, ""
	}

	var prs []linkedPR
	for _, e := range events {
		if e.GetEvent() != "cross-referenced" || e.Source == nil || e.Source.Issue == nil {
			continue
		}
		ref := e.Source.Issue
		if !ref.IsPullRequest() {
			continue
		}
		lp := linkedPR{Number: ref.GetNumber(), State: ref.GetState()}
		if lp.State != "open" {
			pr := getPR(lp.Number)
			if pr == nil {
				continue // couldn't resolve — fail open, skip this one
			}
			lp.Merged = pr.GetMerged()
			lp.ClosedAt = pr.GetClosedAt().Time
		}
		prs = append(prs, lp)
	}
	return linkedPRVerdict(prs, time.Now())
}

// linkedPR is a timeline-linked pull request reduced to the fields the
// availability decision needs.
type linkedPR struct {
	Number   int
	State    string // "open" or "closed"
	Merged   bool
	ClosedAt time.Time
}

// linkedPRVerdict is the pure availability decision over a set of linked PRs: an
// open PR means the issue is taken; a recently closed, unmerged PR means a
// stalled/contested attempt worth a human look. Merged PRs and old closed
// attempts do not block. Kept free of I/O so it is exhaustively unit-testable.
func linkedPRVerdict(prs []linkedPR, now time.Time) (bool, string) {
	for _, pr := range prs {
		if pr.State == "open" {
			return true, fmt.Sprintf("open linked PR #%d", pr.Number)
		}
		if !pr.Merged && now.Sub(pr.ClosedAt) < staleAttemptWindow {
			return true, fmt.Sprintf("recent stalled PR #%d (closed unmerged %s)", pr.Number, pr.ClosedAt.Format("2006-01-02"))
		}
	}
	return false, ""
}

// getPR fetches a single pull request, returning nil on error (fail open).
func (f *QualifiedIssueFinder) getPR(ctx context.Context, project Project, number int) *github.PullRequest {
	var pr *github.PullRequest
	err := f.rateLimiter.executeWithRetry(ctx, fmt.Sprintf("pr %s/%s#%d", project.Org, project.Name, number), func() (*github.Response, error) {
		var apiErr error
		pr, _, apiErr = f.client.PullRequests.Get(ctx, project.Org, project.Name, number)
		return nil, apiErr
	})
	if err != nil {
		log.Printf("pr fetch failed for %s/%s#%d: %v", project.Org, project.Name, number, err)
		return nil
	}
	return pr
}

func (f *QualifiedIssueFinder) evaluateIssue(issue *github.Issue, project Project) *QualifiedIssue {
	labels := getLabelNames(issue.Labels)

	if !f.isQualifiedType(labels) {
		return nil
	}

	if f.hasExcludedLabels(labels) {
		return nil
	}

	// A "help wanted" (or "good first issue") label does not make a support
	// question actionable. Veto on content so the qualifier never surfaces a
	// thread with no concrete deliverable, even when the label set looks good.
	if f.looksLikeQuestion(issue) {
		return nil
	}

	if len(issue.Assignees) > 0 && f.filter.RequireNoAssignee {
		return nil
	}

	qualified := &QualifiedIssue{
		Issue: Issue{
			Project:   project,
			Title:     issue.GetTitle(),
			URL:       issue.GetHTMLURL(),
			Number:    issue.GetNumber(),
			CreatedAt: issue.GetCreatedAt().Time,
			Comments:  issue.GetComments(),
			Labels:    labels,
		},
		WhyGood: []string{},
	}

	qualified.Type = f.determineIssueType(labels)
	qualified.IsConfirmed = hasAnyLabelStr(labels, "confirmed", "triage/accepted", "accepted", "status/confirmed")
	qualified.IsApproved = hasAnyLabelStr(labels, "approved", "planned", "triage/accepted")
	qualified.HasClearRepro = f.hasClearReproduction(issue)
	qualified.HasAcceptanceCrit = f.hasAcceptanceCriteria(issue)
	qualified.Priority = f.extractPriority(labels)
	qualified.MaintainerApproved = qualified.IsConfirmed || qualified.IsApproved

	qualified.QualifiedScore = f.scorer.ScoreIssue(issue, project, qualified)

	return qualified
}

func (s *QualifiedScorer) ScoreIssue(issue *github.Issue, project Project, qualified *QualifiedIssue) QualifiedIssueScore {
	score := QualifiedIssueScore{}

	score.ProjectStars = s.scoreProjectStars(project.Stars)
	score.IsBug = s.scoreIsBug(qualified.Type)
	score.HasClearReproduction = s.scoreClearReproduction(qualified.HasClearRepro)
	score.MaintainerApproved = s.scoreMaintainerApproval(qualified.MaintainerApproved)
	score.HasAcceptanceCriteria = s.scoreAcceptanceCriteria(qualified.HasAcceptanceCrit)
	score.Priority = s.scorePriority(qualified.Priority)
	score.NoAssignee = s.scoreNoAssignee(len(issue.Assignees) == 0)
	score.NoOpenPR = s.scoreNoOpenPR(issue.PullRequestLinks == nil)
	score.LowComments = s.scoreLowComments(issue.GetComments())
	score.GoodDescription = s.scoreDescriptionQuality(issue)
	score.HasCodeSnippets = s.scoreCodeSnippets(issue.GetBody())
	score.RecentActivity = s.scoreRecentActivity(issue.GetCreatedAt().Time)

	impactScore := (score.ProjectStars + score.IsBug + score.HasClearReproduction) / 3
	approvalScore := (score.MaintainerApproved + score.HasAcceptanceCriteria + score.Priority) / 3
	accessibilityScore := (score.NoAssignee + score.NoOpenPR + score.LowComments) / 3
	qualityScore := (score.GoodDescription + score.HasCodeSnippets + score.RecentActivity) / 3

	score.TotalScore = (impactScore*s.config.ImpactWeight +
		approvalScore*s.config.ApprovalWeight +
		accessibilityScore*s.config.AccessibilityWeight +
		qualityScore*s.config.QualityWeight)

	return score
}

func (s *QualifiedScorer) scoreProjectStars(stars int) float64 {
	if stars >= 100000 {
		return 1.0
	} else if stars >= 50000 {
		return 0.9
	} else if stars >= 10000 {
		return 0.8
	} else if stars >= 5000 {
		return 0.7
	} else if stars >= 1000 {
		return 0.6
	}
	return 0.4
}

func (s *QualifiedScorer) scoreIsBug(issueType QualifiedIssueType) float64 {
	if issueType == QualifiedTypeBug {
		return 1.0
	}
	return 0.5
}

func (s *QualifiedScorer) scoreClearReproduction(hasRepro bool) float64 {
	if hasRepro {
		return 1.0
	}
	return 0.3
}

func (s *QualifiedScorer) scoreMaintainerApproval(approved bool) float64 {
	if approved {
		return 1.0
	}
	return 0.2
}

func (s *QualifiedScorer) scoreAcceptanceCriteria(has bool) float64 {
	if has {
		return 1.0
	}
	return 0.4
}

func (s *QualifiedScorer) scorePriority(priority string) float64 {
	switch strings.ToUpper(priority) {
	case "P1", "P0", "CRITICAL":
		return 1.0
	case "P2", "HIGH":
		return 0.8
	case "P3", "MEDIUM":
		return 0.6
	case "P4", "LOW":
		return 0.4
	}
	return 0.5
}

func (s *QualifiedScorer) scoreNoAssignee(noAssignee bool) float64 {
	if noAssignee {
		return 1.0
	}
	return 0.0
}

func (s *QualifiedScorer) scoreNoOpenPR(noPR bool) float64 {
	if noPR {
		return 1.0
	}
	return 0.0
}

func (s *QualifiedScorer) scoreLowComments(comments int) float64 {
	if comments <= 2 {
		return 1.0
	} else if comments <= 5 {
		return 0.8
	} else if comments <= 10 {
		return 0.6
	}
	return 0.3
}

func (s *QualifiedScorer) scoreDescriptionQuality(issue *github.Issue) float64 {
	body := issue.GetBody()
	if body == "" {
		return 0.0
	}

	score := 0.0
	if len(body) >= 100 {
		score += 0.3
	}
	if len(body) >= 300 {
		score += 0.2
	}

	lowerBody := strings.ToLower(body)
	keywords := []string{"expected", "actual", "steps", "reproduce", "reproducibility", "environment"}
	for _, kw := range keywords {
		if strings.Contains(lowerBody, kw) {
			score += 0.1
		}
	}

	if score > 1.0 {
		score = 1.0
	}
	return score
}

func (s *QualifiedScorer) scoreCodeSnippets(body string) float64 {
	if strings.Contains(body, "```") {
		return 1.0
	}
	return 0.3
}

func (s *QualifiedScorer) scoreRecentActivity(createdAt time.Time) float64 {
	age := time.Since(createdAt).Hours()
	if age <= 24*7 {
		return 1.0
	} else if age <= 24*30 {
		return 0.8
	} else if age <= 24*90 {
		return 0.6
	}
	return 0.4
}

func (f *QualifiedIssueFinder) isQualifiedType(labels []string) bool {
	for _, label := range labels {
		labelLower := strings.ToLower(label)
		if strings.Contains(labelLower, "bug") ||
			strings.Contains(labelLower, "feature") ||
			strings.Contains(labelLower, "enhancement") ||
			strings.Contains(labelLower, "help wanted") ||
			strings.Contains(labelLower, "good first issue") {
			return true
		}
	}
	return false
}

func (f *QualifiedIssueFinder) hasExcludedLabels(labels []string) bool {
	excludePatterns := []string{
		"question", "support", "wontfix", "duplicate", "invalid",
		"stale", "needs-triage", "waiting-for-info", "needs info",
		"discussion", "proposal", "rfc",
	}
	for _, label := range labels {
		labelLower := strings.ToLower(label)
		for _, pattern := range excludePatterns {
			if strings.Contains(labelLower, pattern) {
				return true
			}
		}
	}
	return false
}

// looksLikeQuestion reports whether an issue is really a question or support
// request rather than actionable work. Maintainers sometimes tag open-ended
// questions with "help wanted"/"good first issue", so a good label set is not
// enough — we classify on the title and body. Two tiers keep false positives
// low: strong phrases veto on a single hit, soft phrases only when they pile up
// or the title is itself a question with no actionable signal (no repro, no
// code block, no checklist).
func (f *QualifiedIssueFinder) looksLikeQuestion(issue *github.Issue) bool {
	title := strings.ToLower(strings.TrimSpace(issue.GetTitle()))
	body := strings.ToLower(issue.GetBody())

	// Title openers that almost always signal a question, not a task.
	for _, p := range []string{
		"how do", "how to", "how can", "how should", "how does",
		"why does", "why is", "why do", "what is", "what does",
		"is it possible", "is there a way", "is there any",
		"can i ", "can we ", "do i need", "does anyone", "should i",
		"question about", "question:", "help with",
	} {
		if strings.HasPrefix(title, p) {
			return true
		}
	}

	// Strong body phrases: a single occurrence is enough to veto.
	for _, p := range []string{
		"how do i", "how can i", "how should i", "is it possible to",
		"my question is", "just a question", "support request",
		"can someone explain", "am i doing something wrong",
		"is this a bug or", "not sure if this is a bug",
	} {
		if strings.Contains(body, p) {
			return true
		}
	}

	// Soft body phrases: only veto when two or more pile up.
	soft := 0
	for _, p := range []string{
		"is there a way", "any idea", "any help", "please help",
		"need help", "am i missing", "expected behavior?", "am i wrong",
	} {
		if strings.Contains(body, p) {
			soft++
		}
	}
	if soft >= 2 {
		return true
	}

	// Title phrased as a question with no actionable signal in the body. A "?"
	// title that ships repro steps or a code block is usually a real bug report,
	// so those are kept.
	if strings.HasSuffix(title, "?") &&
		!strings.Contains(body, "```") &&
		!f.hasClearReproduction(issue) &&
		!f.hasAcceptanceCriteria(issue) {
		return true
	}

	return false
}

func (f *QualifiedIssueFinder) determineIssueType(labels []string) QualifiedIssueType {
	for _, label := range labels {
		labelLower := strings.ToLower(label)
		if strings.Contains(labelLower, "bug") {
			return QualifiedTypeBug
		}
		if strings.Contains(labelLower, "feature") {
			return QualifiedTypeFeature
		}
		if strings.Contains(labelLower, "enhancement") {
			return QualifiedTypeEnhance
		}
		if strings.Contains(labelLower, "help wanted") {
			return QualifiedTypeHelpWanted
		}
	}
	return QualifiedTypeEnhance
}

func (f *QualifiedIssueFinder) hasClearReproduction(issue *github.Issue) bool {
	body := strings.ToLower(issue.GetBody())
	reproductionKeywords := []string{
		"steps to reproduce", "how to reproduce", "reproduction",
		"1.", "step 1", "expected:", "actual:",
	}
	count := 0
	for _, kw := range reproductionKeywords {
		if strings.Contains(body, kw) {
			count++
		}
	}
	return count >= 2 || strings.Contains(body, "```")
}

func (f *QualifiedIssueFinder) hasAcceptanceCriteria(issue *github.Issue) bool {
	body := strings.ToLower(issue.GetBody())
	criteriaKeywords := []string{
		"acceptance criteria", "definition of done", "success criteria",
		"- [ ]", "- [x]", "checklist", "todo:",
	}
	for _, kw := range criteriaKeywords {
		if strings.Contains(body, kw) {
			return true
		}
	}
	return false
}

func (f *QualifiedIssueFinder) extractPriority(labels []string) string {
	for _, label := range labels {
		labelLower := strings.ToLower(label)
		if strings.Contains(labelLower, "priority/p1") || strings.Contains(labelLower, "priority/p0") {
			return "P1"
		}
		if strings.Contains(labelLower, "priority/p2") {
			return "P2"
		}
		if strings.Contains(labelLower, "priority/p3") {
			return "P3"
		}
		if strings.Contains(labelLower, "priority/p4") {
			return "P4"
		}
		if strings.Contains(labelLower, "critical") {
			return "P1"
		}
		if strings.Contains(labelLower, "high") {
			return "P2"
		}
		if strings.Contains(labelLower, "medium") {
			return "P3"
		}
	}
	return ""
}

func hasAnyLabelStr(labels []string, targets ...string) bool {
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

func (f *QualifiedIssueFinder) FindBugs(ctx context.Context, minScore float64) ([]QualifiedIssue, error) {
	allIssues, err := f.FindQualifiedIssues(ctx, minScore)
	if err != nil {
		return nil, err
	}

	var bugs []QualifiedIssue
	for _, issue := range allIssues {
		if issue.Type == QualifiedTypeBug {
			bugs = append(bugs, issue)
		}
	}
	return bugs, nil
}

func (f *QualifiedIssueFinder) FindFeatures(ctx context.Context, minScore float64) ([]QualifiedIssue, error) {
	allIssues, err := f.FindQualifiedIssues(ctx, minScore)
	if err != nil {
		return nil, err
	}

	var features []QualifiedIssue
	for _, issue := range allIssues {
		if issue.Type == QualifiedTypeFeature || issue.Type == QualifiedTypeEnhance {
			features = append(features, issue)
		}
	}
	return features, nil
}

func (q *QualifiedIssue) GenerateWhyGood() []string {
	var reasons []string

	if q.QualifiedScore.ProjectStars >= 0.8 {
		reasons = append(reasons, fmt.Sprintf("Popular project (%d stars)", q.Project.Stars))
	}

	if q.IsConfirmed {
		reasons = append(reasons, "Confirmed and triaged by maintainers")
	}

	if q.IsApproved {
		reasons = append(reasons, "Approved for implementation")
	}

	if q.HasClearRepro {
		reasons = append(reasons, "Clear reproduction steps available")
	}

	if q.Priority != "" {
		reasons = append(reasons, fmt.Sprintf("Priority: %s", q.Priority))
	}

	if q.QualifiedScore.NoAssignee == 1.0 {
		reasons = append(reasons, "No assignee - available to work on")
	}

	if q.QualifiedScore.LowComments >= 0.8 {
		reasons = append(reasons, "Low competition - few comments")
	}

	if q.Type == QualifiedTypeBug {
		reasons = append(reasons, "Real bug fix - great for resume")
	}

	if q.QualifiedScore.GoodDescription >= 0.5 {
		reasons = append(reasons, "Well documented issue")
	}

	return reasons
}
