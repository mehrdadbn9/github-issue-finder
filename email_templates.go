package main

import (
	"fmt"
	"strings"
	"time"
)

type EmailTemplate struct {
	Subject  string
	HTMLBody string
	TextBody string
}

type ScoreBreakdown struct {
	StarsScore        float64
	CommentsScore     float64
	RecencyScore      float64
	LabelsScore       float64
	DifficultyScore   float64
	DescriptionScore  float64
	ActivityScore     float64
	MaintainerScore   float64
	BonusScore        float64
	TotalScore        float64
	StarsWeight       float64
	CommentsWeight    float64
	RecencyWeight     float64
	LabelsWeight      float64
	DescriptionWeight float64
}

func NewIssueEmailTemplate(issue Issue, breakdown *ScoreBreakdown) *EmailTemplate {
	scoreEmoji := "✨"
	if issue.Score >= 0.8 {
		scoreEmoji = "🔥"
	} else if issue.Score >= 0.6 {
		scoreEmoji = "⭐"
	}

	labelsHTML := ""
	if len(issue.Labels) > 0 {
		for _, label := range issue.Labels {
			labelsHTML += fmt.Sprintf(`<span style="background:#e1e4e8;padding:2px 8px;border-radius:12px;font-size:12px;margin-right:4px;">%s</span>`, label)
		}
	}

	breakdownHTML := ""
	if breakdown != nil {
		breakdownHTML = fmt.Sprintf(`
		<div style="background:#f6f8fa;padding:16px;border-radius:8px;margin-top:20px;">
			<h3 style="margin-top:0;color:#24292e;">Score Breakdown</h3>
			<table style="width:100%%;border-collapse:collapse;">
				<tr><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;">Project Popularity</td><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;text-align:right;">%.2f</td></tr>
				<tr><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;">Competition (Comments)</td><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;text-align:right;">%.2f</td></tr>
				<tr><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;">Recency</td><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;text-align:right;">%.2f</td></tr>
				<tr><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;">Labels Match</td><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;text-align:right;">%.2f</td></tr>
				<tr><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;">Description Quality</td><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;text-align:right;">%.2f</td></tr>
				<tr><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;">Project Activity</td><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;text-align:right;">%.2f</td></tr>
				<tr><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;">Bonus Factors</td><td style="padding:8px 0;border-bottom:1px solid #e1e4e8;text-align:right;">%.2f</td></tr>
				<tr style="font-weight:bold;background:#fff8c5;"><td style="padding:12px 0;">Total Score</td><td style="padding:12px 0;text-align:right;">%.2f</td></tr>
			</table>
		</div>
		`, breakdown.StarsScore, breakdown.CommentsScore, breakdown.RecencyScore,
			breakdown.LabelsScore, breakdown.DescriptionScore, breakdown.ActivityScore,
			breakdown.BonusScore, breakdown.TotalScore)
	}

	htmlBody := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
</head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;font-size:16px;line-height:1.5;color:#24292e;max-width:600px;margin:0 auto;padding:20px;">
	<div style="background:linear-gradient(135deg,#667eea 0%%,#764ba2 100%%);padding:30px;border-radius:12px 12px 0 0;text-align:center;">
		<h1 style="color:#fff;margin:0;font-size:24px;">%s New Issue Found</h1>
		<p style="color:rgba(255,255,255,0.9);margin:10px 0 0;">A great learning opportunity awaits!</p>
	</div>
	
	<div style="background:#fff;border:1px solid #e1e4e8;border-top:none;padding:24px;border-radius:0 0 12px 12px;">
		<h2 style="margin-top:0;color:#0366d6;font-size:20px;">%s</h2>
		
		<div style="margin:16px 0;">
			<span style="display:inline-block;background:#28a745;color:#fff;padding:4px 12px;border-radius:4px;font-weight:bold;">Score: %.2f</span>
			<span style="margin-left:12px;color:#586069;">%s/%s (%d★)</span>
		</div>
		
		<table style="width:100%%;margin:16px 0;">
			<tr>
				<td style="color:#586069;padding:8px 0;width:120px;">Category:</td>
				<td style="padding:8px 0;">%s</td>
			</tr>
			<tr>
				<td style="color:#586069;padding:8px 0;">Comments:</td>
				<td style="padding:8px 0;">%d</td>
			</tr>
			<tr>
				<td style="color:#586069;padding:8px 0;">Created:</td>
				<td style="padding:8px 0;">%s</td>
			</tr>
		</table>
		
		<div style="margin:16px 0;">%s</div>
		
		<a href="%s" style="display:inline-block;background:#0366d6;color:#fff;padding:12px 24px;border-radius:6px;text-decoration:none;font-weight:bold;margin-top:16px;">View Issue →</a>
		
		%s
	</div>
	
	<div style="text-align:center;padding:20px;color:#586069;font-size:14px;">
		<p>GitHub Issue Finder • %s</p>
	</div>
</body>
</html>
	`, scoreEmoji, issue.Title, issue.Score, issue.Project.Org, issue.Project.Name,
		issue.Project.Stars, issue.Project.Category, issue.Comments,
		issue.CreatedAt.Format("January 2, 2006"), labelsHTML, issue.URL,
		breakdownHTML, time.Now().Format("2006"))

	textBody := fmt.Sprintf(`
New Issue Found!

%s (Score: %.2f)

Project: %s/%s (%d stars)
Category: %s
Comments: %d
Created: %s

URL: %s

Labels: %s

---
GitHub Issue Finder • %s
	`, issue.Title, issue.Score, issue.Project.Org, issue.Project.Name,
		issue.Project.Stars, issue.Project.Category, issue.Comments,
		issue.CreatedAt.Format("2006-01-02"), issue.URL,
		strings.Join(issue.Labels, ", "), time.Now().Format("2006-01-02"))

	return &EmailTemplate{
		Subject:  fmt.Sprintf("%s [%.2f] %s", scoreEmoji, issue.Score, truncateString(issue.Title, 50)),
		HTMLBody: htmlBody,
		TextBody: textBody,
	}
}

func DigestEmailTemplate(issues []Issue) *EmailTemplate {
	date := time.Now().Format("January 2, 2006")

	goodFirstIssues := []Issue{}
	otherIssues := []Issue{}

	for _, issue := range issues {
		if issue.IsGoodFirst {
			goodFirstIssues = append(goodFirstIssues, issue)
		} else {
			otherIssues = append(otherIssues, issue)
		}
	}

	var issuesHTML strings.Builder
	issuesHTML.WriteString(`
	</div>
	
	<div style="text-align:center;padding:20px;color:#586069;font-size:14px;">
		<p>GitHub Issue Finder</p>
	</div>
</body>
</html>
	`)

	if len(goodFirstIssues) > 0 {
		issuesHTML.WriteString(`<h2 style="color:#28a745;margin-top:0;">🔥 Good First Issues</h2>`)
		for i, issue := range goodFirstIssues {
			if i >= 10 {
				issuesHTML.WriteString(fmt.Sprintf(`<p style="color:#586069;">... and %d more good first issues</p>`, len(goodFirstIssues)-10))
				break
			}
			issuesHTML.WriteString(fmt.Sprintf(`
			<div style="border:1px solid #e1e4e8;border-radius:8px;padding:16px;margin:12px 0;">
				<h3 style="margin:0 0 8px;color:#0366d6;"><a href="%s" style="color:#0366d6;text-decoration:none;">%s</a></h3>
				<p style="margin:0;color:#586069;font-size:14px;">
					<span style="background:#28a745;color:#fff;padding:2px 8px;border-radius:4px;">%.2f</span>
					%s/%s • %d comments
				</p>
			</div>
			`, issue.URL, issue.Title, issue.Score, issue.Project.Org, issue.Project.Name, issue.Comments))
		}
	}

	if len(otherIssues) > 0 {
		issuesHTML.WriteString(`<h2 style="color:#0366d6;margin-top:24px;">📋 Other Opportunities</h2>`)
		for i, issue := range otherIssues {
			if i >= 5 {
				issuesHTML.WriteString(fmt.Sprintf(`<p style="color:#586069;">... and %d more issues</p>`, len(otherIssues)-5))
				break
			}
			issuesHTML.WriteString(fmt.Sprintf(`
			<div style="border:1px solid #e1e4e8;border-radius:8px;padding:16px;margin:12px 0;">
				<h3 style="margin:0 0 8px;color:#0366d6;"><a href="%s" style="color:#0366d6;text-decoration:none;">%s</a></h3>
				<p style="margin:0;color:#586069;font-size:14px;">
					<span style="background:#0366d6;color:#fff;padding:2px 8px;border-radius:4px;">%.2f</span>
					%s/%s • %d comments
				</p>
			</div>
			`, issue.URL, issue.Title, issue.Score, issue.Project.Org, issue.Project.Name, issue.Comments))
		}
	}

	issuesHTML.WriteString(`
	</div>
	
	<div style="text-align:center;padding:20px;color:#586069;font-size:14px;">
		<p>GitHub Issue Finder</p>
	</div>
</body>
</html>
	`)

	var textBody strings.Builder
	textBody.WriteString(fmt.Sprintf("Daily Issue Digest - %s\n\n%d issues found\n\n", date, len(issues)))

	if len(goodFirstIssues) > 0 {
		textBody.WriteString("🔥 Good First Issues:\n")
		for i, issue := range goodFirstIssues {
			if i >= 10 {
				textBody.WriteString(fmt.Sprintf("... and %d more\n", len(goodFirstIssues)-10))
				break
			}
			textBody.WriteString(fmt.Sprintf("- [%.2f] %s\n  %s/%s • %s\n\n", issue.Score, issue.Title, issue.Project.Org, issue.Project.Name, issue.URL))
		}
	}

	if len(otherIssues) > 0 {
		textBody.WriteString("\n📋 Other Opportunities:\n")
		for i, issue := range otherIssues {
			if i >= 5 {
				textBody.WriteString(fmt.Sprintf("... and %d more\n", len(otherIssues)-5))
				break
			}
			textBody.WriteString(fmt.Sprintf("- [%.2f] %s\n  %s/%s • %s\n\n", issue.Score, issue.Title, issue.Project.Org, issue.Project.Name, issue.URL))
		}
	}

	return &EmailTemplate{
		Subject:  fmt.Sprintf("📰 Daily Issue Digest - %s (%d issues)", date, len(issues)),
		HTMLBody: issuesHTML.String(),
		TextBody: textBody.String(),
	}
}

func AssignmentConfirmationTemplate(issue Issue) *EmailTemplate {
	htmlBody := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
</head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;font-size:16px;line-height:1.5;color:#24292e;max-width:600px;margin:0 auto;padding:20px;">
	<div style="background:linear-gradient(135deg,#28a745 0%%,#20863c 100%%);padding:30px;border-radius:12px 12px 0 0;text-align:center;">
		<h1 style="color:#fff;margin:0;font-size:24px;">✅ Assignment Confirmed!</h1>
		<p style="color:rgba(255,255,255,0.9);margin:10px 0 0;">You've been assigned to this issue</p>
	</div>
	
	<div style="background:#fff;border:1px solid #e1e4e8;border-top:none;padding:24px;border-radius:0 0 12px 12px;">
		<h2 style="margin-top:0;color:#0366d6;">%s</h2>
		
		<div style="margin:16px 0;">
			<span style="margin-left:12px;color:#586069;">%s/%s</span>
		</div>
		
		<a href="%s" style="display:inline-block;background:#28a745;color:#fff;padding:12px 24px;border-radius:6px;text-decoration:none;font-weight:bold;margin-top:16px;">Start Working →</a>
		
		<div style="margin-top:24px;padding:16px;background:#f6f8fa;border-radius:8px;">
			<p style="margin:0;color:#586069;font-size:14px;">
				<strong>Next steps:</strong><br>
				1. Clone the repository<br>
				2. Create a branch for your changes<br>
				3. Make your contributions<br>
				4. Submit a pull request
			</p>
		</div>
	</div>
	
	<div style="text-align:center;padding:20px;color:#586069;font-size:14px;">
		<p>GitHub Issue Finder • %s</p>
	</div>
</body>
</html>
	`, issue.Title, issue.Project.Org, issue.Project.Name, issue.URL, time.Now().Format("2006-01-02"))

	textBody := fmt.Sprintf(`
Assignment Confirmed!

You've been assigned to: %s

Project: %s/%s
URL: %s

Next steps:
1. Clone the repository
2. Create a branch for your changes
3. Make your contributions
4. Submit a pull request

---
GitHub Issue Finder • %s
	`, issue.Title, issue.Project.Org, issue.Project.Name, issue.URL, time.Now().Format("2006-01-02"))

	return &EmailTemplate{
		Subject:  fmt.Sprintf("✅ Assignment Confirmed: %s", truncateString(issue.Title, 50)),
		HTMLBody: htmlBody,
		TextBody: textBody,
	}
}

func AssignmentRequestTemplate(issue Issue) *EmailTemplate {
	htmlBody := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
</head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;font-size:16px;line-height:1.5;color:#24292e;max-width:600px;margin:0 auto;padding:20px;">
	<div style="background:linear-gradient(135deg,#f39c12 0%%,#e67e22 100%%);padding:30px;border-radius:12px 12px 0 0;text-align:center;">
		<h1 style="color:#fff;margin:0;font-size:24px;">📤 Assignment Request Sent</h1>
		<p style="color:rgba(255,255,255,0.9);margin:10px 0 0;">Waiting for maintainer approval</p>
	</div>
	
	<div style="background:#fff;border:1px solid #e1e4e8;border-top:none;padding:24px;border-radius:0 0 12px 12px;">
		<h2 style="margin-top:0;color:#0366d6;">%s</h2>
		
		<div style="margin:16px 0;">
			<span style="margin-left:12px;color:#586069;">%s/%s</span>
		</div>
		
		<a href="%s" style="display:inline-block;background:#f39c12;color:#fff;padding:12px 24px;border-radius:6px;text-decoration:none;font-weight:bold;margin-top:16px;">View Issue →</a>
		
		<div style="margin-top:24px;padding:16px;background:#fff8e1;border-radius:8px;border-left:4px solid #f39c12;">
			<p style="margin:0;color:#586069;font-size:14px;">
				<strong>Note:</strong> The maintainer will review your request. You'll receive another notification when the assignment is confirmed.
			</p>
		</div>
	</div>
	
	<div style="text-align:center;padding:20px;color:#586069;font-size:14px;">
		<p>GitHub Issue Finder • %s</p>
	</div>
</body>
</html>
	`, issue.Title, issue.Project.Org, issue.Project.Name, issue.URL, time.Now().Format("2006-01-02"))

	textBody := fmt.Sprintf(`
Assignment Request Sent

Issue: %s
Project: %s/%s
URL: %s

The maintainer will review your request. You'll receive another notification when the assignment is confirmed.

---
GitHub Issue Finder • %s
	`, issue.Title, issue.Project.Org, issue.Project.Name, issue.URL, time.Now().Format("2006-01-02"))

	return &EmailTemplate{
		Subject:  fmt.Sprintf("📤 Assignment Requested: %s", truncateString(issue.Title, 50)),
		HTMLBody: htmlBody,
		TextBody: textBody,
	}
}

func QualifiedIssueEmailTemplate(issue QualifiedIssue) *EmailTemplate {
	scoreEmoji := "⭐"
	if issue.QualifiedScore.TotalScore >= 0.8 {
		scoreEmoji = "🔥"
	}

	var labelsHTML strings.Builder
	relevantLabels := filterRelevantLabels(issue.Labels)
	for _, label := range relevantLabels {
		labelsHTML.WriteString(fmt.Sprintf(`<span style="background:#e1e4e8;padding:2px 8px;border-radius:12px;font-size:12px;margin-right:4px;">%s</span>`, label))
	}

	whyGoodHTML := ""
	whyGood := issue.GenerateWhyGood()
	if len(whyGood) > 0 {
		whyGoodHTML = `<div style="margin-top:16px;padding:16px;background:#f0fff4;border-radius:8px;border-left:4px solid #28a745;"><h4 style="margin:0 0 8px;color:#28a745;">Why it's a good fit:</h4><ul style="margin:0;padding-left:20px;">`
		for _, reason := range whyGood {
			whyGoodHTML += fmt.Sprintf(`<li style="color:#24292e;margin:4px 0;">%s</li>`, reason)
		}
		whyGoodHTML += `</ul></div>`
	}

	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", issue.Project.Org, issue.Project.Name)

	htmlBody := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
</head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;font-size:16px;line-height:1.5;color:#24292e;max-width:600px;margin:0 auto;padding:20px;">
	<div style="background:linear-gradient(135deg,#667eea 0%%,#764ba2 100%%);padding:30px;border-radius:12px 12px 0 0;text-align:center;">
		<h1 style="color:#fff;margin:0;font-size:24px;">%s Qualified Issue Found</h1>
		<p style="color:rgba(255,255,255,0.9);margin:10px 0 0;">A resume-worthy opportunity!</p>
	</div>
	
	<div style="background:#fff;border:1px solid #e1e4e8;border-top:none;padding:24px;border-radius:0 0 12px 12px;">
		<h2 style="margin-top:0;color:#0366d6;font-size:20px;">%s</h2>
		
		<div style="margin:16px 0;">
			<span style="display:inline-block;background:#28a745;color:#fff;padding:4px 12px;border-radius:4px;font-weight:bold;">Score: %.2f</span>
			<span style="display:inline-block;background:#0366d6;color:#fff;padding:4px 12px;border-radius:4px;margin-left:8px;">%s</span>
			<span style="margin-left:12px;color:#586069;">%s/%s (%s ⭐)</span>
		</div>
		
		<table style="width:100%%;margin:16px 0;">
			<tr>
				<td style="color:#586069;padding:8px 0;width:120px;">Type:</td>
				<td style="padding:8px 0;">%s</td>
			</tr>
			<tr>
				<td style="color:#586069;padding:8px 0;">Category:</td>
				<td style="padding:8px 0;">%s</td>
			</tr>
			<tr>
				<td style="color:#586069;padding:8px 0;">Comments:</td>
				<td style="padding:8px 0;">%d</td>
			</tr>
		</table>
		
		<div style="margin:16px 0;">%s</div>
		
		<div style="margin:16px 0;">
			<a href="%s" style="display:inline-block;background:#0366d6;color:#fff;padding:12px 24px;border-radius:6px;text-decoration:none;font-weight:bold;margin-right:8px;">Open Issue →</a>
			<a href="https://github.com/%s/%s" style="display:inline-block;background:#24292e;color:#fff;padding:12px 24px;border-radius:6px;text-decoration:none;font-weight:bold;">View Repo</a>
		</div>
		
		%s
		
		<div style="margin-top:24px;padding:16px;background:#f6f8fa;border-radius:8px;">
			<p style="margin:0 0 8px;color:#586069;font-size:14px;"><strong>Quick Clone:</strong></p>
			<code style="display:block;background:#24292e;color:#fff;padding:12px;border-radius:4px;font-size:13px;overflow-x:auto;">git clone %s</code>
		</div>
	</div>
	
	<div style="text-align:center;padding:20px;color:#586069;font-size:14px;">
		<p>GitHub Issue Finder • %s</p>
	</div>
</body>
</html>
	`, scoreEmoji, issue.Title, issue.QualifiedScore.TotalScore, strings.Title(string(issue.Type)),
		issue.Project.Org, issue.Project.Name, formatStars(issue.Project.Stars),
		strings.Title(string(issue.Type)), issue.Project.Category, issue.Comments,
		labelsHTML.String(), issue.URL, issue.Project.Org, issue.Project.Name,
		whyGoodHTML, cloneURL, time.Now().Format("2006"))

	textBody := fmt.Sprintf(`
Qualified Issue Found!

%s (Score: %.2f)
Type: %s

Project: %s/%s (%s stars)
Category: %s
Comments: %d

URL: %s

Why it's good:
%s

Quick Clone: git clone %s

---
GitHub Issue Finder • %s
	`, issue.Title, issue.QualifiedScore.TotalScore, strings.Title(string(issue.Type)),
		issue.Project.Org, issue.Project.Name, formatStars(issue.Project.Stars),
		issue.Project.Category, issue.Comments, issue.URL,
		strings.Join(whyGood, "\n- "), cloneURL, time.Now().Format("2006-01-02"))

	return &EmailTemplate{
		Subject:  fmt.Sprintf("%s [%.2f] %s", scoreEmoji, issue.QualifiedScore.TotalScore, truncateString(issue.Title, 50)),
		HTMLBody: htmlBody,
		TextBody: textBody,
	}
}
