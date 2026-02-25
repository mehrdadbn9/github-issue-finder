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
		if qualified != nil && qualified.QualifiedScore.TotalScore >= minScore {
			qualifiedIssues = append(qualifiedIssues, *qualified)
		}
	}

	return qualifiedIssues
}

func (f *QualifiedIssueFinder) evaluateIssue(issue *github.Issue, project Project) *QualifiedIssue {
	labels := getLabelNames(issue.Labels)

	if !f.isQualifiedType(labels) {
		return nil
	}

	if f.hasExcludedLabels(labels) {
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
