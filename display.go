package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func DisplayQualifiedIssues(issues []QualifiedIssue, emailCount int) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("🎯 QUALIFIED ISSUES - Resume-Worthy Opportunities")
	fmt.Println(strings.Repeat("=", 80))

	highImpact := []QualifiedIssue{}
	mediumImpact := []QualifiedIssue{}
	lowImpact := []QualifiedIssue{}

	for _, issue := range issues {
		if issue.QualifiedScore.TotalScore >= 0.8 {
			highImpact = append(highImpact, issue)
		} else if issue.QualifiedScore.TotalScore >= 0.6 {
			mediumImpact = append(mediumImpact, issue)
		} else {
			lowImpact = append(lowImpact, issue)
		}
	}

	if len(highImpact) > 0 {
		fmt.Println()
		fmt.Println("🔥 HIGH IMPACT (Score >= 0.8)")
		fmt.Println(strings.Repeat("-", 80))
		for i, issue := range highImpact {
			displayQualifiedIssueCard(issue, i+1)
		}
	}

	if len(mediumImpact) > 0 {
		fmt.Println()
		fmt.Println("⭐ MEDIUM IMPACT (Score 0.6 - 0.79)")
		fmt.Println(strings.Repeat("-", 80))
		for i, issue := range mediumImpact {
			if i >= 10 {
				fmt.Printf("\n   ... and %d more medium impact issues\n", len(mediumImpact)-10)
				break
			}
			displayQualifiedIssueCard(issue, i+1)
		}
	}

	if len(lowImpact) > 0 {
		fmt.Println()
		fmt.Println("📋 LOWER PRIORITY (Score < 0.6)")
		fmt.Println(strings.Repeat("-", 80))
		for i, issue := range lowImpact {
			if i >= 5 {
				fmt.Printf("\n   ... and %d more lower priority issues\n", len(lowImpact)-5)
				break
			}
			displayCompactQualifiedIssue(issue, i+1)
		}
	}

	fmt.Println()
	fmt.Println("📊 SUMMARY")
	fmt.Printf("   High Impact: %d | Medium: %d | Low: %d\n", len(highImpact), len(mediumImpact), len(lowImpact))
	fmt.Printf("   Total Qualified: %d\n", len(issues))

	if emailCount > 0 {
		fmt.Println()
		fmt.Println("🔔 NOTIFICATIONS SENT")
		fmt.Printf("   Email: %d (score > 0.7)\n", emailCount)
		fmt.Printf("   Local: %d (all qualified)\n", len(issues))
	}

	fmt.Println(strings.Repeat("=", 80))
}

func displayQualifiedIssueCard(issue QualifiedIssue, num int) {
	emoji := "🔥"
	if issue.QualifiedScore.TotalScore < 0.8 {
		emoji = "⭐"
	}

	fmt.Printf("\n%s [%d] %s (Score: %.2f)\n", emoji, num, issue.Title, issue.QualifiedScore.TotalScore)
	fmt.Printf("   📦 %s/%s (%s ⭐) | %s", issue.Project.Org, issue.Project.Name, formatStars(issue.Project.Stars), issue.Project.Category)
	if issue.Priority != "" {
		fmt.Printf(" | Priority: %s", issue.Priority)
	}
	fmt.Println()

	fmt.Printf("   📝 Type: %s", titleCase(string(issue.Type)))
	if len(issue.Labels) > 0 {
		relevantLabels := filterRelevantLabels(issue.Labels)
		if len(relevantLabels) > 0 {
			fmt.Printf(" | Labels: %s", strings.Join(relevantLabels, ", "))
		}
	}
	fmt.Println()

	fmt.Printf("   👤 Assignee: None | PRs: 0 | Comments: %d\n", issue.Comments)

	fmt.Printf("   🔗 %s\n", issue.URL)

	whyGood := issue.GenerateWhyGood()
	if len(whyGood) > 0 {
		fmt.Println("   Why it's good:")
		for _, reason := range whyGood {
			fmt.Printf("   • %s\n", reason)
		}
	}

	fmt.Println(strings.Repeat("-", 80))
}

func displayCompactQualifiedIssue(issue QualifiedIssue, num int) {
	emoji := "✨"
	fmt.Printf("\n%s [%d] %s (Score: %.2f)\n", emoji, num, issue.Title, issue.QualifiedScore.TotalScore)
	fmt.Printf("   %s/%s | Type: %s\n", issue.Project.Org, issue.Project.Name, issue.Type)
	fmt.Printf("   %s\n", issue.URL)
}

func formatStars(stars int) string {
	if stars >= 100000 {
		return fmt.Sprintf("%dk", stars/1000)
	} else if stars >= 1000 {
		return fmt.Sprintf("%.1fk", float64(stars)/1000)
	}
	return fmt.Sprintf("%d", stars)
}

func titleCase(s string) string {
	var b strings.Builder
	prevLetter := false
	for _, r := range s {
		if !prevLetter {
			b.WriteString(strings.ToUpper(string(r)))
		} else {
			b.WriteRune(r)
		}
		prevLetter = (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
	}
	return b.String()
}

func filterRelevantLabels(labels []string) []string {
	relevant := []string{}
	relevantPatterns := []string{
		"confirmed", "accepted", "approved", "help wanted",
		"priority", "bug", "feature", "enhancement",
	}
	for _, label := range labels {
		labelLower := strings.ToLower(label)
		for _, pattern := range relevantPatterns {
			if strings.Contains(labelLower, pattern) {
				relevant = append(relevant, label)
				break
			}
		}
	}
	return relevant
}

func DisplayPartitionedIssues(goodFirstIssues, otherIssues, assignedIssues []Issue) {
	DisplayPartitionedIssuesWithConfig(goodFirstIssues, otherIssues, assignedIssues, nil)
}

func DisplayPartitionedIssuesWithConfig(goodFirstIssues, otherIssues, assignedIssues []Issue, config *DisplayConfig) {
	if config == nil {
		config = &DisplayConfig{
			Mode:               "partitioned",
			MaxGoodFirstIssues: 15,
			MaxOtherIssues:     10,
			MaxAssignedIssues:  10,
			ShowScoreBreakdown: true,
		}
	}

	fmt.Printf("\n%s\n", "ISSUE FINDER RESULTS")
	fmt.Println(strings.Repeat("=", 80))

	sort.Slice(goodFirstIssues, func(i, j int) bool {
		return goodFirstIssues[i].Score > goodFirstIssues[j].Score
	})
	sort.Slice(otherIssues, func(i, j int) bool {
		return otherIssues[i].Score > otherIssues[j].Score
	})
	sort.Slice(assignedIssues, func(i, j int) bool {
		return assignedIssues[i].Score > assignedIssues[j].Score
	})

	printSectionHeader("GOOD FIRST ISSUES", len(goodFirstIssues), "🔥")
	fmt.Println("(Issues with good-first-issue + confirmed/triage-accepted labels)")
	fmt.Println(strings.Repeat("-", 80))

	for i, issue := range goodFirstIssues {
		if i >= config.MaxGoodFirstIssues {
			fmt.Printf("\n   ... and %d more good first issues\n", len(goodFirstIssues)-config.MaxGoodFirstIssues)
			break
		}
		printIssueCardWithScore(issue, i+1, "✅", config.ShowScoreBreakdown)
	}

	printSectionHeader("OTHER OPPORTUNITIES", len(otherIssues), "📋")
	fmt.Println("(Bugs, enhancements, help wanted without GFI label)")
	fmt.Println(strings.Repeat("-", 80))

	if len(otherIssues) > 0 {
		categorized := categorizeOtherIssues(otherIssues)
		if len(categorized["bug"]) > 0 {
			fmt.Printf("\n  🐛 Bug Issues (%d)\n", len(categorized["bug"]))
			for i, issue := range categorized["bug"] {
				if i >= 5 {
					fmt.Printf("     ... and %d more\n", len(categorized["bug"])-5)
					break
				}
				printCompactIssueWithScore(issue, "🔴")
			}
		}
		if len(categorized["enhancement"]) > 0 {
			fmt.Printf("\n  ✨ Enhancement Issues (%d)\n", len(categorized["enhancement"]))
			for i, issue := range categorized["enhancement"] {
				if i >= 5 {
					fmt.Printf("     ... and %d more\n", len(categorized["enhancement"])-5)
					break
				}
				printCompactIssueWithScore(issue, "🟢")
			}
		}
		if len(categorized["help"]) > 0 {
			fmt.Printf("\n  🆘 Help Wanted (%d)\n", len(categorized["help"]))
			for i, issue := range categorized["help"] {
				if i >= 5 {
					fmt.Printf("     ... and %d more\n", len(categorized["help"])-5)
					break
				}
				printCompactIssueWithScore(issue, "🟡")
			}
		}
		if len(categorized["other"]) > 0 {
			fmt.Printf("\n  📌 Other Issues (%d)\n", len(categorized["other"]))
			for i, issue := range categorized["other"] {
				if i >= 5 {
					fmt.Printf("     ... and %d more\n", len(categorized["other"])-5)
					break
				}
				printCompactIssueWithScore(issue, "⚪")
			}
		}
	}

	printSectionHeader("YOUR ASSIGNED ISSUES", len(assignedIssues), "👤")
	for i, issue := range assignedIssues {
		if i >= config.MaxAssignedIssues {
			fmt.Printf("\n   ... and %d more assigned issues\n", len(assignedIssues)-config.MaxAssignedIssues)
			break
		}
		printIssueCardWithScore(issue, i+1, "📌", config.ShowScoreBreakdown)
	}

	fmt.Printf("\n%s\n", strings.Repeat("=", 80))
	printSummary(goodFirstIssues, otherIssues, assignedIssues)
}

func printSectionHeader(title string, count int, emoji string) {
	fmt.Printf("\n\n%s %s (%d issues)\n", emoji, title, count)
	fmt.Println(strings.Repeat("-", 80))
	if count == 0 {
		fmt.Println("  No issues in this category")
	}
}

func printIssueCardWithScore(issue Issue, num int, emoji string, showBreakdown bool) {
	scoreEmoji := getScoreEmoji(issue.Score)

	fmt.Printf("\n%s [%d] %s\n", scoreEmoji, num, issue.Title)
	fmt.Printf("   Score: %.2f %s\n", issue.Score, getScoreLabel(issue.Score))
	fmt.Printf("   Project: %s/%s (%d★) | %s\n", issue.Project.Org, issue.Project.Name, issue.Project.Stars, issue.Project.Category)
	fmt.Printf("   Comments: %d | Created: %s\n", issue.Comments, issue.CreatedAt.Format("2006-01-02"))
	fmt.Printf("   URL: %s\n", issue.URL)
	if len(issue.Labels) > 0 {
		fmt.Printf("   Labels: %s\n", strings.Join(issue.Labels, ", "))
	}

	if showBreakdown && issue.Score > 0 {
		printMiniScoreBreakdown(issue)
	}
}

func printMiniScoreBreakdown(issue Issue) {
	fmt.Printf("   Score factors: ")
	var factors []string

	if issue.Project.Stars >= 10000 {
		factors = append(factors, fmt.Sprintf("popular(%d★)", issue.Project.Stars))
	}
	if issue.Comments <= 2 {
		factors = append(factors, "low-competition")
	}
	age := time.Since(issue.CreatedAt).Hours()
	if age <= 72 {
		factors = append(factors, "recent")
	}
	if issue.IsGoodFirst {
		factors = append(factors, "good-first-issue")
	}
	for _, label := range issue.Labels {
		labelLower := strings.ToLower(label)
		if strings.Contains(labelLower, "confirmed") || strings.Contains(labelLower, "accepted") {
			factors = append(factors, "confirmed")
			break
		}
	}
	if len(factors) == 0 {
		factors = append(factors, "standard")
	}
	fmt.Printf("%s\n", strings.Join(factors, ", "))
}

func printCompactIssueWithScore(issue Issue, emoji string) {
	scoreEmoji := getScoreEmoji(issue.Score)
	fmt.Printf("   %s %s [%.2f] - %s/%s\n", emoji, scoreEmoji, issue.Score, issue.Project.Org, issue.Project.Name)
	fmt.Printf("      %s\n", issue.URL)
}

func getScoreEmoji(score float64) string {
	if score >= 0.8 {
		return "🔥"
	} else if score >= 0.6 {
		return "⭐"
	}
	return "✨"
}

func getScoreLabel(score float64) string {
	if score >= 0.8 {
		return "(Excellent)"
	} else if score >= 0.6 {
		return "(Good)"
	}
	return "(Fair)"
}

func categorizeOtherIssues(issues []Issue) map[string][]Issue {
	categorized := make(map[string][]Issue)
	categorized["bug"] = []Issue{}
	categorized["enhancement"] = []Issue{}
	categorized["help"] = []Issue{}
	categorized["other"] = []Issue{}

	for _, issue := range issues {
		hasBug := false
		hasEnhancement := false
		hasHelp := false

		for _, label := range issue.Labels {
			labelLower := strings.ToLower(label)
			if strings.Contains(labelLower, "bug") {
				hasBug = true
			}
			if strings.Contains(labelLower, "enhancement") || strings.Contains(labelLower, "feature") {
				hasEnhancement = true
			}
			if strings.Contains(labelLower, "help wanted") || strings.Contains(labelLower, "help-wanted") {
				hasHelp = true
			}
		}

		if hasBug {
			categorized["bug"] = append(categorized["bug"], issue)
		} else if hasEnhancement {
			categorized["enhancement"] = append(categorized["enhancement"], issue)
		} else if hasHelp {
			categorized["help"] = append(categorized["help"], issue)
		} else {
			categorized["other"] = append(categorized["other"], issue)
		}
	}

	return categorized
}

func printSummary(goodFirstIssues, otherIssues, assignedIssues []Issue) {
	total := len(goodFirstIssues) + len(otherIssues) + len(assignedIssues)
	fmt.Printf("\n📊 SUMMARY\n")
	fmt.Printf("   Good First Issues: %d\n", len(goodFirstIssues))
	fmt.Printf("   Other Opportunities: %d\n", len(otherIssues))
	fmt.Printf("   Your Assigned Issues: %d\n", len(assignedIssues))
	fmt.Printf("   Total Displayed: %d\n", total)

	if total > 0 {
		avgScore := calculateAverageScore(goodFirstIssues, otherIssues, assignedIssues)
		fmt.Printf("   Average Score: %.2f\n", avgScore)

		topIssue := getTopIssue(goodFirstIssues, otherIssues)
		if topIssue != nil {
			fmt.Printf("\n   🏆 Top Issue: %s (%.2f)\n", truncateString(topIssue.Title, 40), topIssue.Score)
			fmt.Printf("      %s\n", topIssue.URL)
		}
	}
}

func calculateAverageScore(issues ...[]Issue) float64 {
	var total float64
	var count int
	for _, issueList := range issues {
		for _, issue := range issueList {
			total += issue.Score
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func getTopIssue(issueLists ...[]Issue) *Issue {
	var top *Issue
	for _, issues := range issueLists {
		for i := range issues {
			if top == nil || issues[i].Score > top.Score {
				top = &issues[i]
			}
		}
	}
	return top
}

func DisplayEligibleIssues(eligible, ineligible []Issue) {
	fmt.Printf("\n%s\n", "ISSUE ELIGIBILITY CHECK")
	fmt.Println(strings.Repeat("=", 80))

	printSectionHeader("ELIGIBLE FOR ASSIGNMENT", len(eligible), "✅")
	for i, issue := range eligible {
		if i >= 15 {
			fmt.Printf("\n   ... and %d more eligible issues\n", len(eligible)-15)
			break
		}
		printIssueCardWithScore(issue, i+1, "🔥", true)
	}

	printSectionHeader("NOT ELIGIBLE", len(ineligible), "⚠️")
	for i, issue := range ineligible {
		if i >= 10 {
			fmt.Printf("\n   ... and %d more ineligible issues\n", len(ineligible)-10)
			break
		}
		fmt.Printf("\n[%d] %s\n", i+1, issue.Title)
		fmt.Printf("   URL: %s\n", issue.URL)
		fmt.Printf("   Labels: %s\n", strings.Join(issue.Labels, ", "))
	}
}

func DisplayIssueDigest(issues []Issue, date string) {
	fmt.Printf("\n%s\n", "DAILY ISSUE DIGEST")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("Date: %s\n", date)

	if len(issues) == 0 {
		fmt.Println("\nNo issues found for today's digest.")
		return
	}

	fmt.Printf("\nFound %d issues\n", len(issues))

	goodFirst := []Issue{}
	other := []Issue{}

	for _, issue := range issues {
		if issue.IsGoodFirst {
			goodFirst = append(goodFirst, issue)
		} else {
			other = append(other, issue)
		}
	}

	if len(goodFirst) > 0 {
		fmt.Printf("\n🔥 Good First Issues (%d):\n", len(goodFirst))
		for i, issue := range goodFirst {
			if i >= 5 {
				fmt.Printf("   ... and %d more\n", len(goodFirst)-5)
				break
			}
			printCompactIssueWithScore(issue, "✅")
		}
	}

	if len(other) > 0 {
		fmt.Printf("\n📋 Other Issues (%d):\n", len(other))
		for i, issue := range other {
			if i >= 5 {
				fmt.Printf("   ... and %d more\n", len(other)-5)
				break
			}
			printCompactIssueWithScore(issue, "📌")
		}
	}
}

func DisplayIssuesJSON(issues []Issue) {
	fmt.Printf("{\"issues\":[")
	for i, issue := range issues {
		if i > 0 {
			fmt.Printf(",")
		}
		fmt.Printf("{\"title\":\"%s\",\"url\":\"%s\",\"score\":%.2f,\"project\":\"%s/%s\",\"stars\":%d,\"comments\":%d,\"is_good_first\":%v}",
			escapeJSON(issue.Title), issue.URL, issue.Score, issue.Project.Org, issue.Project.Name, issue.Project.Stars, issue.Comments, issue.IsGoodFirst)
	}
	fmt.Printf("],\"total\":%d}\n", len(issues))
}

func escapeJSON(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}
