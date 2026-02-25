package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *MCPServer) RegisterPrompts(srv *mcp.Server) {
	srv.AddPrompt(&mcp.Prompt{
		Name:        "find_resume_worthy_issues",
		Description: "Find issues that are good for resume building based on project popularity, issue complexity, and contribution potential",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "min_stars",
				Description: "Minimum number of stars for the project (default: 1000)",
				Required:    false,
			},
			{
				Name:        "category",
				Description: "Filter by project category (kubernetes, networking, storage, monitoring, security, gitops, infrastructure, backup, go-core)",
				Required:    false,
			},
			{
				Name:        "difficulty",
				Description: "Filter by difficulty level (easy, medium, hard)",
				Required:    false,
			},
		},
	}, s.handleFindResumeWorthyIssuesPrompt)

	srv.AddPrompt(&mcp.Prompt{
		Name:        "analyze_and_suggest",
		Description: "Analyze a specific issue and suggest an approach for contributing",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "owner",
				Description: "Repository owner (e.g., kubernetes)",
				Required:    true,
			},
			{
				Name:        "repo",
				Description: "Repository name (e.g., kubernetes)",
				Required:    true,
			},
			{
				Name:        "issue_number",
				Description: "Issue number to analyze",
				Required:    true,
			},
		},
	}, s.handleAnalyzeAndSuggestPrompt)

	srv.AddPrompt(&mcp.Prompt{
		Name:        "create_contribution_plan",
		Description: "Create a detailed plan for contributing to a project, including steps, timeline, and best practices",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "owner",
				Description: "Repository owner (e.g., kubernetes)",
				Required:    true,
			},
			{
				Name:        "repo",
				Description: "Repository name (e.g., kubernetes)",
				Required:    true,
			},
			{
				Name:        "issue_number",
				Description: "Issue number to create a plan for",
				Required:    true,
			},
			{
				Name:        "experience_level",
				Description: "Your experience level with the project (beginner, intermediate, advanced)",
				Required:    false,
			},
		},
	}, s.handleCreateContributionPlanPrompt)

	srv.AddPrompt(&mcp.Prompt{
		Name:        "generate_issue_comment",
		Description: "Generate a professional comment for an issue, expressing interest or asking for assignment",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "owner",
				Description: "Repository owner (e.g., kubernetes)",
				Required:    true,
			},
			{
				Name:        "repo",
				Description: "Repository name (e.g., kubernetes)",
				Required:    true,
			},
			{
				Name:        "issue_number",
				Description: "Issue number to comment on",
				Required:    true,
			},
			{
				Name:        "comment_type",
				Description: "Type of comment: 'interest' (express interest), 'assignment' (ask for assignment), 'clarification' (ask questions), 'solution' (propose solution)",
				Required:    false,
			},
			{
				Name:        "skills",
				Description: "Your relevant skills or experience (comma-separated)",
				Required:    false,
			},
		},
	}, s.handleGenerateIssueCommentPrompt)
}

func (s *MCPServer) handleFindResumeWorthyIssuesPrompt(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := req.Params.Arguments

	minStars := "1000"
	if v, ok := args["min_stars"]; ok && v != "" {
		minStars = v
	}

	category := ""
	if v, ok := args["category"]; ok {
		category = v
	}

	difficulty := "medium"
	if v, ok := args["difficulty"]; ok && v != "" {
		difficulty = v
	}

	promptText := fmt.Sprintf(`You are helping find open source issues that would be valuable for building a strong resume.

Please use the following tools to find resume-worthy issues:
1. Use find_issues with min_score=0.6 and appropriate filters
2. Use find_good_first_issues with confirmed_only=true for beginner-friendly options
3. Use find_confirmed_issues for issues that are ready to be worked on

Filter Criteria:
- Minimum project stars: %s
- Category filter: %s (leave empty if not specified)
- Difficulty preference: %s

Resume-worthy criteria to consider:
1. Project popularity (stars >= 10000 is excellent, >= 1000 is good)
2. Issue has "confirmed" or "approved" labels from maintainers
3. Issue is a bug fix (shows problem-solving) or feature (shows ability to add functionality)
4. Project is well-known in the industry (Kubernetes, Prometheus, etc.)
5. Issue has clear requirements and acceptance criteria
6. No assignee currently assigned

After finding issues, analyze each one and explain:
- Why it's good for resume building
- What skills it demonstrates
- Estimated complexity and time commitment
- Any concerns or red flags

Format the output as a prioritized list with recommendations.`, minStars, category, difficulty)

	if category != "" {
		promptText = strings.Replace(promptText, "(leave empty if not specified)", "", 1)
	}

	return &mcp.GetPromptResult{
		Description: "Find resume-worthy open source issues",
		Messages: []*mcp.PromptMessage{
			{
				Role:    "user",
				Content: &mcp.TextContent{Text: promptText},
			},
		},
	}, nil
}

func (s *MCPServer) handleAnalyzeAndSuggestPrompt(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := req.Params.Arguments

	owner, ok := args["owner"]
	if !ok || owner == "" {
		return nil, fmt.Errorf("owner is required")
	}

	repo, ok := args["repo"]
	if !ok || repo == "" {
		return nil, fmt.Errorf("repo is required")
	}

	issueNumber, ok := args["issue_number"]
	if !ok || issueNumber == "" {
		return nil, fmt.Errorf("issue_number is required")
	}

	promptText := fmt.Sprintf(`You are analyzing GitHub issue %s/%s#%s to help a developer understand and contribute to it.

Please follow these steps:
1. Use the get_issue_details tool with owner="%s", repo="%s", issue_number=%s to fetch the issue details
2. Use the analyze_issue tool with the same parameters to get a resume-worthiness assessment
3. Optionally, use the repo://%s/%s resource to understand the project context

Based on the information gathered, provide:

1. **Issue Summary**
   - What the issue is about
   - Current state (open/closed, has assignee, labels)
   - Project context and popularity

2. **Technical Analysis**
   - What part of the codebase is likely affected
   - Complexity level (beginner/intermediate/advanced)
   - Required skills and knowledge
   - Dependencies or blockers

3. **Suggested Approach**
   - Step-by-step plan to solve the issue
   - Files to examine first
   - Tests to write or update
   - Documentation changes needed

4. **Contribution Tips**
   - Best practices for this project
   - How to test changes locally
   - PR submission guidelines
   - Communication recommendations

5. **Risk Assessment**
   - Potential pitfalls
   - Breaking changes to watch for
   - Questions to ask maintainers

Be specific and actionable in your suggestions.`, owner, repo, issueNumber, owner, repo, issueNumber, owner, repo)

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Analyze issue %s/%s#%s and suggest contribution approach", owner, repo, issueNumber),
		Messages: []*mcp.PromptMessage{
			{
				Role:    "user",
				Content: &mcp.TextContent{Text: promptText},
			},
		},
	}, nil
}

func (s *MCPServer) handleCreateContributionPlanPrompt(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := req.Params.Arguments

	owner, ok := args["owner"]
	if !ok || owner == "" {
		return nil, fmt.Errorf("owner is required")
	}

	repo, ok := args["repo"]
	if !ok || repo == "" {
		return nil, fmt.Errorf("repo is required")
	}

	issueNumber, ok := args["issue_number"]
	if !ok || issueNumber == "" {
		return nil, fmt.Errorf("issue_number is required")
	}

	experienceLevel := "beginner"
	if v, ok := args["experience_level"]; ok && v != "" {
		experienceLevel = v
	}

	promptText := fmt.Sprintf(`You are creating a detailed contribution plan for GitHub issue %s/%s#%s.

Experience Level: %s

Please gather information using:
1. get_issue_details tool with owner="%s", repo="%s", issue_number=%s
2. analyze_issue tool with the same parameters
3. repo://%s/%s resource for project context
4. config:// resource for understanding configured settings

Create a comprehensive plan with:

## 1. Preparation Phase (Day 1-2)
- [ ] Fork and clone the repository
- [ ] Set up development environment
- [ ] Read contributing guidelines (CONTRIBUTING.md)
- [ ] Review code of conduct
- [ ] Understand project structure
- [ ] Build the project locally

## 2. Understanding Phase (Day 2-3)
- [ ] Read and understand the issue thoroughly
- [ ] Identify affected components/files
- [ ] Study related code and tests
- [ ] Review similar past PRs for patterns
- [ ] Document questions for maintainers

## 3. Implementation Phase
For a %s contributor:
%s

## 4. Testing Phase
- [ ] Write/update unit tests
- [ ] Write/update integration tests if needed
- [ ] Manual testing checklist
- [ ] Run all existing tests to ensure no regressions
- [ ] Check code coverage

## 5. Review & Submit Phase
- [ ] Self-review checklist
- [ ] Update documentation
- [ ] Write clear PR description
- [ ] Link issue in PR
- [ ] Request review

## 6. Post-Submit Phase
- [ ] Respond to review feedback
- [ ] Make requested changes
- [ ] Celebrate when merged! 🎉

## Timeline Estimate
Based on experience level and issue complexity

## Resources
- Project documentation links
- Relevant code files to study
- Similar merged PRs for reference
- Communication channels (Slack/Discord/mailing list)

Make the plan specific to the issue and actionable.`, owner, repo, issueNumber, experienceLevel, owner, repo, issueNumber, owner, repo, experienceLevel, getExperienceLevelGuidance(experienceLevel))

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Create contribution plan for %s/%s#%s", owner, repo, issueNumber),
		Messages: []*mcp.PromptMessage{
			{
				Role:    "user",
				Content: &mcp.TextContent{Text: promptText},
			},
		},
	}, nil
}

func getExperienceLevelGuidance(level string) string {
	switch level {
	case "beginner":
		return `- Start with small, focused changes
- Ask questions early and often
- Request mentorship from maintainers
- Take time to understand the codebase
- Consider pair programming sessions`
	case "intermediate":
		return `- Break work into logical commits
- Consider edge cases and error handling
- Write comprehensive tests
- Think about backwards compatibility
- Prepare for detailed code review`
	case "advanced":
		return `- Consider architectural implications
- Evaluate performance impact
- Think about scalability
- Consider multi-region/multi-cluster scenarios
- Prepare benchmark results if applicable`
	default:
		return `- Follow project conventions
- Communicate with maintainers
- Test thoroughly`
	}
}

func (s *MCPServer) handleGenerateIssueCommentPrompt(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := req.Params.Arguments

	owner, ok := args["owner"]
	if !ok || owner == "" {
		return nil, fmt.Errorf("owner is required")
	}

	repo, ok := args["repo"]
	if !ok || repo == "" {
		return nil, fmt.Errorf("repo is required")
	}

	issueNumber, ok := args["issue_number"]
	if !ok || issueNumber == "" {
		return nil, fmt.Errorf("issue_number is required")
	}

	commentType := "interest"
	if v, ok := args["comment_type"]; ok && v != "" {
		commentType = v
	}

	skills := ""
	if v, ok := args["skills"]; ok {
		skills = v
	}

	commentGuidance := getCommentGuidance(commentType)

	promptText := fmt.Sprintf(`You are generating a professional GitHub comment for issue %s/%s#%s.

Comment Type: %s
%s

First, use the generate_comment tool with:
- owner: "%s"
- repo: "%s"  
- issue_number: %s
- comment_type: "%s"

Then use get_issue_details to understand the issue context.

%s

Guidelines for the comment:
1. Be professional and concise
2. Show genuine interest in helping
3. Demonstrate relevant knowledge
4. Avoid demanding or entitled tone
5. Ask meaningful questions if unclear
6. Respect maintainers' time

%s

The comment should be ready to copy-paste, with any [PLACEHOLDERS] clearly marked for the user to customize.`, owner, repo, issueNumber, commentType, commentGuidance, owner, repo, issueNumber, commentType, getSkillsPrompt(skills), getCommentTypeInstructions(commentType))

	return &mcp.GetPromptResult{
		Description: fmt.Sprintf("Generate %s comment for %s/%s#%s", commentType, owner, repo, issueNumber),
		Messages: []*mcp.PromptMessage{
			{
				Role:    "user",
				Content: &mcp.TextContent{Text: promptText},
			},
		},
	}, nil
}

func getCommentGuidance(commentType string) string {
	switch commentType {
	case "interest":
		return "This comment expresses interest in working on the issue."
	case "assignment":
		return "This comment formally requests to be assigned to the issue."
	case "clarification":
		return "This comment asks thoughtful questions to better understand the issue."
	case "solution":
		return "This comment proposes a potential solution approach."
	default:
		return "This comment engages with the issue professionally."
	}
}

func getSkillsPrompt(skills string) string {
	if skills == "" {
		return "No specific skills provided - generate a general professional comment."
	}
	return fmt.Sprintf("The user has the following relevant skills: %s\nIncorporate these naturally into the comment.", skills)
}

func getCommentTypeInstructions(commentType string) string {
	switch commentType {
	case "interest":
		return `Format for interest comment:
- Brief introduction (if first contribution to project)
- Express interest in the specific issue
- Mention relevant experience/skills
- Ask about approach or timeline if appropriate
- Offer to discuss further`

	case "assignment":
		return `Format for assignment request:
- Reference the issue clearly
- Explain why you're a good fit
- Mention relevant background/experience
- Propose a timeline for completion
- Use "/assign" or "/assign-me" if the project supports it`

	case "clarification":
		return `Format for clarification comment:
- Acknowledge what you understand
- Ask specific, well-researched questions
- Propose potential interpretations
- Show you've done homework (read docs, code)
- Be open to guidance`

	case "solution":
		return `Format for solution proposal:
- Summarize your understanding of the issue
- Outline proposed approach at high level
- Mention key files/components affected
- Ask for feedback before starting work
- Be open to alternative approaches`

	default:
		return `General guidelines:
- Be professional and respectful
- Stay on topic
- Add value to the discussion
- Follow project communication norms`
	}
}
