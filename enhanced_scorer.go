package main

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/v58/github"
)

type EnhancedScorer struct {
	weights            map[string]float64
	config             *ScoringConfig
	maintainerPatterns []*regexp.Regexp
	activityThreshold  time.Duration
}

func NewEnhancedScorer() *EnhancedScorer {
	return NewEnhancedScorerWithConfig(nil)
}

func NewEnhancedScorerWithConfig(config *ScoringConfig) *EnhancedScorer {
	if config == nil {
		config = &ScoringConfig{
			StarWeight:               0.08,
			CommentWeight:            0.15,
			RecencyWeight:            0.15,
			LabelWeight:              0.20,
			DifficultyWeight:         0.12,
			DescriptionQualityWeight: 0.10,
			ActivityWeight:           0.10,
			MaintainerWeight:         0.10,
			ContributorFriendlyBonus: 0.15,
			WeekendBonus:             0.05,
			MaxScore:                 1.5,
		}
	}

	return &EnhancedScorer{
		config: config,
		weights: map[string]float64{
			"stars_factor":       config.StarWeight,
			"comments_factor":    config.CommentWeight,
			"recency_factor":     config.RecencyWeight,
			"labels_factor":      config.LabelWeight,
			"difficulty_factor":  config.DifficultyWeight,
			"description_factor": config.DescriptionQualityWeight,
			"activity_factor":    config.ActivityWeight,
			"maintainer_factor":  config.MaintainerWeight,
		},
		maintainerPatterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)/(assign|cla|lgtm|approve|hold|retest|close)`),
			regexp.MustCompile(`(?i)@kubernetes/(sig|wg|committee|org)-`),
			regexp.MustCompile(`(?i)@golang/`),
		},
		activityThreshold: 30 * 24 * time.Hour,
	}
}

func (s *EnhancedScorer) ScoreIssueEnhanced(issue *github.Issue, project Project, repoActivity *RepoActivityInfo) float64 {
	return s.ScoreIssueWithBreakdown(issue, project, repoActivity).TotalScore
}

func (s *EnhancedScorer) ScoreIssueWithBreakdown(issue *github.Issue, project Project, repoActivity *RepoActivityInfo) *ScoreBreakdown {
	breakdown := &ScoreBreakdown{}

	baseScorer := NewIssueScorer()
	breakdown.StarsScore = baseScorer.normalizeStars(project.Stars) * s.weights["stars_factor"]
	breakdown.CommentsScore = baseScorer.normalizeComments(*issue.Comments) * s.weights["comments_factor"]
	breakdown.RecencyScore = baseScorer.normalizeRecency(issue.CreatedAt.Time) * s.weights["recency_factor"]
	breakdown.LabelsScore = baseScorer.normalizeLabels(issue.Labels) * s.weights["labels_factor"]
	breakdown.DifficultyScore = baseScorer.normalizeDifficulty(issue.Labels, safeString(issue.Body)) * s.weights["difficulty_factor"]
	breakdown.DescriptionScore = s.scoreDescriptionQuality(issue) * s.weights["description_factor"]

	if repoActivity != nil {
		breakdown.ActivityScore = s.scoreProjectActivity(repoActivity) * s.weights["activity_factor"]
		breakdown.MaintainerScore = s.scoreMaintainerResponsiveness(repoActivity) * s.weights["maintainer_factor"]
	}

	breakdown.BonusScore = s.applyBonusModifiers(issue, project)
	penalty := s.calculatePenaltyModifiers(issue)

	breakdown.TotalScore = breakdown.StarsScore + breakdown.CommentsScore + breakdown.RecencyScore +
		breakdown.LabelsScore + breakdown.DifficultyScore + breakdown.DescriptionScore +
		breakdown.ActivityScore + breakdown.MaintainerScore + breakdown.BonusScore - penalty

	breakdown.TotalScore = s.clampScore(breakdown.TotalScore)

	return breakdown
}

func (s *EnhancedScorer) clampScore(score float64) float64 {
	if score > s.config.MaxScore {
		score = s.config.MaxScore
	}
	if score < 0 {
		score = 0
	}
	return score
}

func (s *EnhancedScorer) scoreDescriptionQuality(issue *github.Issue) float64 {
	body := safeString(issue.Body)
	title := safeString(issue.Title)

	if body == "" {
		return 0.0
	}

	score := 0.0

	if len(body) >= 100 {
		score += 0.2
	}
	if len(body) >= 300 {
		score += 0.2
	}

	codeBlockPattern := regexp.MustCompile("```[\\s\\S]*?```")
	if codeBlockPattern.MatchString(body) {
		score += 0.2
	}

	stepsKeywords := []string{"steps to reproduce", "how to reproduce", "reproduc", "expected", "actual"}
	lowerBody := strings.ToLower(body)
	for _, kw := range stepsKeywords {
		if strings.Contains(lowerBody, kw) {
			score += 0.1
		}
	}

	acceptanceKeywords := []string{"acceptance criteria", "definition of done", "success criteria", "todo:", "checklist"}
	for _, kw := range acceptanceKeywords {
		if strings.Contains(lowerBody, kw) {
			score += 0.15
		}
	}

	scopeKeywords := []string{"file:", "func:", "package:", "method:", "struct:", "interface:", "in pkg/", "in cmd/"}
	scopeCount := 0
	for _, kw := range scopeKeywords {
		if strings.Contains(lowerBody, kw) || strings.Contains(strings.ToLower(title), kw) {
			scopeCount++
		}
	}
	if scopeCount >= 2 {
		score += 0.15
	}

	if strings.Contains(lowerBody, "good first issue") || strings.Contains(lowerBody, "beginner") {
		score += s.config.ContributorFriendlyBonus
	}

	if score > 1.0 {
		score = 1.0
	}

	return score
}

func (s *EnhancedScorer) scoreProjectActivity(activity *RepoActivityInfo) float64 {
	if activity == nil {
		return 0.5
	}

	score := 0.0

	daysSinceCommit := time.Since(activity.LastCommit).Hours() / 24
	if daysSinceCommit <= 1 {
		score += 0.4
	} else if daysSinceCommit <= 7 {
		score += 0.3
	} else if daysSinceCommit <= 30 {
		score += 0.2
	} else if daysSinceCommit <= 90 {
		score += 0.1
	}

	if activity.CommitsLastMonth >= 50 {
		score += 0.3
	} else if activity.CommitsLastMonth >= 20 {
		score += 0.2
	} else if activity.CommitsLastMonth >= 5 {
		score += 0.1
	}

	if activity.PRActivityLastMonth >= 20 {
		score += 0.2
	} else if activity.PRActivityLastMonth >= 10 {
		score += 0.1
	}

	if activity.OpenPRs > 0 && activity.OpenPRs < 100 {
		score += 0.1
	}

	if score > 1.0 {
		score = 1.0
	}

	return score
}

func (s *EnhancedScorer) scoreMaintainerResponsiveness(activity *RepoActivityInfo) float64 {
	if activity == nil {
		return 0.5
	}

	score := 0.0

	if activity.AvgIssueResponseTime > 0 {
		hours := activity.AvgIssueResponseTime.Hours()
		if hours <= 4 {
			score += 0.4
		} else if hours <= 24 {
			score += 0.3
		} else if hours <= 72 {
			score += 0.2
		} else if hours <= 168 {
			score += 0.1
		}
	}

	if activity.PRReviewTime > 0 {
		hours := activity.PRReviewTime.Hours()
		if hours <= 24 {
			score += 0.3
		} else if hours <= 72 {
			score += 0.2
		} else if hours <= 168 {
			score += 0.1
		}
	}

	if score > 1.0 {
		score = 1.0
	}

	return score
}

func (s *EnhancedScorer) scoreTimeFactors(issue *github.Issue) float64 {
	score := 0.0

	createdAt := issue.CreatedAt.Time
	hour := createdAt.Hour()
	dayOfWeek := createdAt.Weekday()

	if dayOfWeek == time.Saturday || dayOfWeek == time.Sunday {
		score += s.config.WeekendBonus
	}

	if hour >= 9 && hour <= 17 {
		score += 0.02
	}

	age := time.Since(createdAt).Hours()
	if age <= 24 {
		score += 0.15
	} else if age <= 72 {
		score += 0.10
	} else if age <= 168 {
		score += 0.05
	}

	if age > 720 && age < 4320 && *issue.Comments <= 3 {
		score += 0.10
	}

	return score
}

func (s *EnhancedScorer) scoreContributorFriendliness(issue *github.Issue, project Project) float64 {
	score := 0.0
	title := strings.ToLower(safeString(issue.Title))
	body := strings.ToLower(safeString(issue.Body))
	combined := title + " " + body

	if hasLabel(issue.Labels, "good first issue") || hasLabel(issue.Labels, "good-first-issue") {
		score += s.config.ContributorFriendlyBonus
	}

	if hasLabel(issue.Labels, "help wanted") || hasLabel(issue.Labels, "help-wanted") {
		score += 0.10
	}

	beginnerLabels := []string{"beginner", "starter", "easy", "newcomer", "first-timers-only"}
	for _, label := range beginnerLabels {
		if hasLabel(issue.Labels, label) {
			score += s.config.ContributorFriendlyBonus
			break
		}
	}

	if strings.Contains(combined, "beginner") || strings.Contains(combined, "newcomer") {
		score += 0.05
	}

	easyKeywords := []string{"quick", "easy", "simple", "trivial", "small", "minor", "typo", "spelling"}
	for _, kw := range easyKeywords {
		if strings.Contains(combined, kw) {
			score += 0.05
			break
		}
	}

	if len(issue.Assignees) == 0 {
		score += 0.10
	}

	return score
}

func (s *EnhancedScorer) applyBonusModifiers(issue *github.Issue, project Project) float64 {
	var bonus float64
	title := strings.ToLower(safeString(issue.Title))
	body := strings.ToLower(safeString(issue.Body))
	combined := title + " " + body

	if hasLabel(issue.Labels, "good first issue") || hasLabel(issue.Labels, "good-first-issue") {
		bonus += 0.30
	}

	if hasConfirmedLabel(issue.Labels) {
		bonus += 0.35
	}

	if hasLabel(issue.Labels, "help wanted") || hasLabel(issue.Labels, "help-wanted") {
		bonus += 0.10
	}

	beginnerLabels := []string{"beginner", "starter", "easy", "newcomer", "first-timers-only"}
	for _, label := range beginnerLabels {
		if hasLabel(issue.Labels, label) {
			bonus += 0.15
			break
		}
	}

	if strings.Contains(combined, "documentation") || strings.Contains(combined, "docs") ||
		hasLabel(issue.Labels, "documentation") || strings.Contains(title, "doc:") {
		bonus += 0.15
	}

	cncfProjects := []string{
		"kubernetes", "prometheus", "etcd", "istio", "cilium", "containerd", "grpc",
		"helm", "dapr", "keda", "argo", "rancher", "velero", "traefik", "flux",
		"knative", "opa", "cni", "cri-o", "runc", "coredns", "envoy", "linkerd",
		"crossplane", "backstage", "opentelemetry",
	}
	for _, p := range cncfProjects {
		if strings.Contains(strings.ToLower(project.Name), p) {
			bonus += 0.10
			break
		}
	}

	if strings.Contains(strings.ToLower(project.Category), "tls") ||
		strings.Contains(strings.ToLower(project.Category), "security") {
		bonus += 0.10
	}

	if strings.Contains(combined, "tls") || strings.Contains(combined, "ssl") ||
		strings.Contains(combined, "certificate") || strings.Contains(combined, "https") {
		bonus += 0.10
	}

	easyKeywords := []string{"quick", "easy", "simple", "trivial", "small", "minor", "typo", "spelling"}
	for _, kw := range easyKeywords {
		if strings.Contains(combined, kw) {
			bonus += 0.05
			break
		}
	}

	age := time.Since(issue.CreatedAt.Time).Hours()
	if age > 720 && age < 4320 && *issue.Comments <= 3 {
		bonus += 0.10
	}

	if *issue.Comments <= 2 {
		bonus += 0.05
	}

	bonus += s.scoreTimeFactors(issue)
	bonus += s.scoreContributorFriendliness(issue, project)

	return bonus
}

func (s *EnhancedScorer) calculatePenaltyModifiers(issue *github.Issue) float64 {
	var penalty float64
	title := strings.ToLower(safeString(issue.Title))
	body := strings.ToLower(safeString(issue.Body))
	combined := title + " " + body

	cloudKeywords := []string{
		"gcp", "google cloud", "compute engine", "gke", "cloud sql", "bigquery", "pubsub",
		"aws", "amazon web", "ec2", "s3 bucket", "lambda", "eks", "rds", "dynamodb",
		"azure", "microsoft azure", "aks", "azure functions", "azure storage",
	}
	for _, kw := range cloudKeywords {
		if strings.Contains(combined, kw) {
			penalty += 0.50
			break
		}
	}

	if hasAnyLabel(issue.Labels, "provider:google", "provider:aws", "provider:azure",
		"area/gcp", "area/aws", "area/azure") {
		penalty += 0.50
	}

	if hasLabel(issue.Labels, "needs-triage") || hasLabel(issue.Labels, "needs triage") {
		penalty += 0.15
	}

	blockedKeywords := []string{"blocked", "waiting for", "needs approval", "on hold", "pending"}
	for _, kw := range blockedKeywords {
		if strings.Contains(combined, kw) {
			penalty += 0.20
			break
		}
	}

	if hasAnyLabel(issue.Labels, "wontfix", "invalid", "duplicate", "wont-fix") {
		penalty += 0.50
	}

	if hasAnyLabel(issue.Labels, "needs info", "needs-information", "waitingforinfo") {
		penalty += 0.15
	}

	complexKeywords := []string{"complex", "difficult", "challenging", "breaking change", "refactor entire"}
	for _, kw := range complexKeywords {
		if strings.Contains(combined, kw) {
			penalty += 0.10
			break
		}
	}

	if issue.PullRequestLinks != nil {
		penalty += 0.30
	}

	if len(issue.Assignees) > 0 {
		penalty += 0.25
	}

	return penalty
}

func (s *EnhancedScorer) applyPenaltyModifiers(issue *github.Issue, score float64) float64 {
	penalty := s.calculatePenaltyModifiers(issue)
	return score - penalty
}

type IssueScore struct {
	Total       float64
	Breakdown   DetailedScoreBreakdown
	Grade       string
	Recommended bool
}

type DetailedScoreBreakdown struct {
	ProjectStars        float64
	BugSeverity         float64
	ProductionImpact    float64
	MaintainerConfirmed float64
	HasReproSteps       float64
	NotDuplicate        float64
	NoAssignee          float64
	NoOpenPR            float64
	LowCompetition      float64
	ClearDescription    float64
	HasCodeLocation     float64
}

func CalculateScore(issue *github.Issue, repo *Project, prs []PullRequest) IssueScore {
	score := IssueScore{
		Breakdown: DetailedScoreBreakdown{},
	}

	if repo == nil {
		repo = &Project{}
	}

	stars := repo.Stars
	if stars >= 50000 {
		score.Breakdown.ProjectStars = 15.0
	} else if stars >= 10000 {
		score.Breakdown.ProjectStars = 10.0
	} else if stars >= 1000 {
		score.Breakdown.ProjectStars = 5.0
	} else {
		score.Breakdown.ProjectStars = 2.0
	}

	if hasConfirmedLabel(issue.Labels) {
		score.Breakdown.MaintainerConfirmed = 15.0
	} else if hasLabel(issue.Labels, "triage/accepted") || hasLabel(issue.Labels, "accepted") {
		score.Breakdown.MaintainerConfirmed = 10.0
	}

	body := safeString(issue.Body)
	lowerBody := strings.ToLower(body)

	if strings.Contains(lowerBody, "steps to reproduce") ||
		strings.Contains(lowerBody, "reproduc") ||
		strings.Contains(lowerBody, "```") {
		score.Breakdown.HasReproSteps = 10.0
	} else if len(body) > 200 {
		score.Breakdown.HasReproSteps = 5.0
	}

	if len(issue.Assignees) == 0 {
		score.Breakdown.NoAssignee = 10.0
	}

	if issue.PullRequestLinks == nil {
		score.Breakdown.NoOpenPR = 5.0
	}

	if *issue.Comments <= 3 {
		score.Breakdown.LowCompetition = 5.0
	} else if *issue.Comments <= 5 {
		score.Breakdown.LowCompetition = 2.0
	}

	if len(body) >= 100 {
		score.Breakdown.ClearDescription = 3.0
	}
	if len(body) >= 300 {
		score.Breakdown.ClearDescription = 5.0
	}

	scopeKeywords := []string{"file:", "func:", "package:", "method:", "struct:", "in pkg/", "in cmd/"}
	for _, kw := range scopeKeywords {
		if strings.Contains(lowerBody, kw) {
			score.Breakdown.HasCodeLocation += 2.0
		}
	}
	if score.Breakdown.HasCodeLocation > 5.0 {
		score.Breakdown.HasCodeLocation = 5.0
	}

	total := score.Breakdown.ProjectStars +
		score.Breakdown.BugSeverity +
		score.Breakdown.ProductionImpact +
		score.Breakdown.MaintainerConfirmed +
		score.Breakdown.HasReproSteps +
		score.Breakdown.NotDuplicate +
		score.Breakdown.NoAssignee +
		score.Breakdown.NoOpenPR +
		score.Breakdown.LowCompetition +
		score.Breakdown.ClearDescription +
		score.Breakdown.HasCodeLocation

	score.Total = total / 100.0

	if hasGoodFirstIssueLabel(issue.Labels) {
		score.Total += 0.15
	}
	if hasLabel(issue.Labels, "help wanted") || hasLabel(issue.Labels, "help-wanted") {
		score.Total += 0.10
	}

	title := strings.ToLower(safeString(issue.Title))
	combined := title + " " + lowerBody

	cloudKeywords := []string{
		"gcp", "google cloud", "compute engine", "gke", "cloud sql", "bigquery",
		"aws", "amazon web", "ec2", "s3 bucket", "lambda", "eks", "rds",
		"azure", "microsoft azure", "aks", "azure functions",
	}
	for _, kw := range cloudKeywords {
		if strings.Contains(combined, kw) {
			score.Total -= 0.30
			break
		}
	}

	if hasAnyLabel(issue.Labels, "wontfix", "invalid", "duplicate", "wont-fix") {
		score.Total -= 0.40
	}

	if score.Total > 1.0 {
		score.Total = 1.0
	}
	if score.Total < 0 {
		score.Total = 0
	}

	score.Grade = score.GetGrade()
	score.Recommended = score.Total >= 0.75

	return score
}

func (s *IssueScore) GetGrade() string {
	switch {
	case s.Total >= 0.90:
		return "A+"
	case s.Total >= 0.85:
		return "A"
	case s.Total >= 0.80:
		return "B+"
	case s.Total >= 0.75:
		return "B"
	case s.Total >= 0.70:
		return "C"
	case s.Total >= 0.60:
		return "D"
	default:
		return "F"
	}
}

type PullRequest struct {
	Number int
	State  string
	User   string
}

type RepoActivityInfo struct {
	LastCommit           time.Time
	CommitsLastMonth     int
	PRActivityLastMonth  int
	OpenPRs              int
	OpenIssues           int
	AvgIssueResponseTime time.Duration
	PRReviewTime         time.Duration
	MaintainerCount      int
	RecentMergeCount     int
}

func (s *EnhancedScorer) ScoreIssueSimple(issue *github.Issue, project Project) float64 {
	return s.ScoreIssueEnhanced(issue, project, nil)
}
