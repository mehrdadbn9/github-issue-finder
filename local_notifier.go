package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type LocalNotifier struct {
	emailConfig       *EmailConfig
	emailSender       *EmailSender
	logFile           *os.File
	notificationsFile *os.File
}

func NewLocalNotifier(emailConfig *EmailConfig) (*LocalNotifier, error) {
	logDir := strings.TrimSpace(os.Getenv("LOG_DIR"))
	if logDir == "" {
		logDir = filepath.Join(".", "logs")
	}

	absDir, err := filepath.Abs(logDir)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve log directory %q: %w", logDir, err)
	}

	if mkErr := os.MkdirAll(absDir, 0755); mkErr != nil {
		return nil, fmt.Errorf("failed to create log directory %q: %w", absDir, mkErr)
	}

	notifier := &LocalNotifier{
		emailConfig: emailConfig,
	}

	logFile, err := os.OpenFile(filepath.Join(absDir, "issues.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	notifier.logFile = logFile

	notificationsFile, err := os.OpenFile(filepath.Join(absDir, "notifications.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open notifications file: %w", err)
	}
	notifier.notificationsFile = notificationsFile

	if emailConfig != nil && emailConfig.SMTPHost != "" {
		notifier.emailSender = NewEmailSender(emailConfig, emailConfig.MaxPerHour, emailConfig.MaxPerDay)
		if err := notifier.emailSender.VerifyConfig(); err != nil {
			log.Printf("[Notifier] Email verification failed: %v", err)
		} else {
			log.Printf("[Notifier] Email configured successfully for %s", emailConfig.ToEmail)
		}
	}

	return notifier, nil
}

func (n *LocalNotifier) Close() {
	if n.logFile != nil {
		n.logFile.Close()
	}
	if n.notificationsFile != nil {
		n.notificationsFile.Close()
	}
}

func (n *LocalNotifier) logToFile(message string) {
	if n.logFile != nil {
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		logEntry := fmt.Sprintf("[%s] %s\n", timestamp, message)
		n.logFile.WriteString(logEntry)
	}
}

func (n *LocalNotifier) logToNotificationsFile(title, url string, score float64, priority string) {
	if n.notificationsFile != nil {
		notification := fmt.Sprintf("%s|%s|%.2f|%s\n", title, url, score, priority)
		n.notificationsFile.WriteString(notification)
		n.notificationsFile.Sync()
	}
}

func (n *LocalNotifier) SendIssuesAlert(issues []Issue) error {
	if len(issues) == 0 {
		return nil
	}

	log.Printf("[Notifier] Processing %d issues for local logging", len(issues))
	n.logToFile(fmt.Sprintf("Found %d new issues", len(issues)))

	for _, issue := range issues {
		priority := "Medium"
		if issue.Score >= 0.8 {
			priority = "High"
		} else if issue.Score < 0.6 {
			priority = "Low"
		}

		n.logToConsole(issue)
		n.logToNotificationsFile(issue.Title, issue.URL, issue.Score, priority)
	}

	if n.emailSender != nil && n.emailConfig != nil {
		if n.emailConfig.Mode == "digest" {
			log.Printf("[Notifier] Digest mode enabled - skipping instant email")
			return nil
		}

		if err := n.sendEmailAlert(issues); err != nil {
			n.logToFile(fmt.Sprintf("Failed to send email: %v", err))
			log.Printf("[Notifier] Failed to send email: %v", err)
		}
	}

	return nil
}

func (n *LocalNotifier) logToConsole(issue Issue) {
	emoji := ""
	if issue.Score >= 0.8 {
		emoji = "🔥"
	} else if issue.Score >= 0.6 {
		emoji = "⭐"
	} else {
		emoji = "✨"
	}

	fmt.Printf("\n%s %s\n", emoji, issue.Project.Category)
	fmt.Printf("Score: %.2f | Stars: %d | Comments: %d\n", issue.Score, issue.Project.Stars, issue.Comments)
	fmt.Printf("Title: %s\n", issue.Title)
	fmt.Printf("URL: %s\n", issue.URL)
	if len(issue.Labels) > 0 {
		fmt.Printf("Labels: %s\n", strings.Join(issue.Labels, ", "))
	}
	fmt.Printf("Created: %s\n", issue.CreatedAt.Format("2006-01-02"))
	fmt.Println(strings.Repeat("-", 80))
}

func (n *LocalNotifier) sendEmailAlert(issues []Issue) error {
	if len(issues) == 0 {
		return nil
	}

	for _, issue := range issues {
		breakdown := &ScoreBreakdown{
			TotalScore: issue.Score,
		}

		if err := n.emailSender.SendNewIssueEmail(issue, breakdown); err != nil {
			log.Printf("[Notifier] Failed to send email for issue %s: %v", issue.URL, err)
			continue
		}

		n.logToFile(fmt.Sprintf("Email sent for issue: %s", issue.URL))
	}

	return nil
}

func (n *LocalNotifier) SendDigestEmail(issues []Issue) error {
	if n.emailSender == nil {
		return fmt.Errorf("email sender not configured")
	}

	if len(issues) == 0 {
		log.Printf("[Notifier] No issues for digest")
		return nil
	}

	if err := n.emailSender.SendDigestEmail(issues); err != nil {
		return fmt.Errorf("failed to send digest email: %w", err)
	}

	n.logToFile(fmt.Sprintf("Digest email sent with %d issues", len(issues)))
	log.Printf("[Notifier] Digest email sent to %s", n.emailConfig.ToEmail)
	return nil
}

func (n *LocalNotifier) SendAssignmentConfirmation(issue Issue) error {
	if n.emailSender == nil {
		return nil
	}

	if err := n.emailSender.SendAssignmentConfirmationEmail(issue); err != nil {
		return fmt.Errorf("failed to send assignment confirmation: %w", err)
	}

	n.logToFile(fmt.Sprintf("Assignment confirmation sent for: %s", issue.URL))
	return nil
}

func (n *LocalNotifier) SendAssignmentRequest(issue Issue) error {
	if n.emailSender == nil {
		return nil
	}

	if err := n.emailSender.SendAssignmentRequestEmail(issue); err != nil {
		return fmt.Errorf("failed to send assignment request notification: %w", err)
	}

	n.logToFile(fmt.Sprintf("Assignment request notification sent for: %s", issue.URL))
	return nil
}

func (n *LocalNotifier) TestEmail() error {
	if n.emailSender == nil {
		return fmt.Errorf("email sender not configured")
	}

	testIssue := Issue{
		Title:     "Test Issue - Email Configuration Working",
		URL:       "https://github.com/test/test/issues/1",
		Score:     0.85,
		CreatedAt: time.Now(),
		Comments:  2,
		Labels:    []string{"good first issue", "help wanted"},
		Project: Project{
			Org:      "test",
			Name:     "test-repo",
			Category: "Test",
			Stars:    1000,
		},
	}

	return n.emailSender.SendNewIssueEmail(testIssue, &ScoreBreakdown{TotalScore: 0.85})
}

func (n *LocalNotifier) LogError(message string) {
	log.Printf("ERROR: %s", message)
	n.logToFile(fmt.Sprintf("ERROR: %s", message))
}

func (n *LocalNotifier) LogInfo(message string) {
	log.Printf("INFO: %s", message)
	n.logToFile(fmt.Sprintf("INFO: %s", message))
}

func (n *LocalNotifier) GetEmailStats() map[string]interface{} {
	if n.emailSender == nil {
		return map[string]interface{}{
			"enabled": false,
		}
	}
	return n.emailSender.GetStats()
}

func (n *LocalNotifier) SendQualifiedIssueEmail(issue QualifiedIssue) error {
	if n.emailSender == nil {
		return fmt.Errorf("email sender not configured")
	}

	canSend, reason := n.emailSender.CanSend(EmailTypeNewIssue, issue.URL)
	if !canSend {
		return fmt.Errorf("cannot send: %s", reason)
	}

	template := QualifiedIssueEmailTemplate(issue)
	err := n.emailSender.SendEmail(template.Subject, template.HTMLBody, template.TextBody)
	if err != nil {
		return err
	}

	n.emailSender.RecordSent(EmailTypeNewIssue, issue.URL)
	n.logToFile(fmt.Sprintf("Qualified issue email sent: %s", issue.URL))
	return nil
}

func (n *LocalNotifier) SendDesktopNotification(issue QualifiedIssue) error {
	title := fmt.Sprintf("🎯 Qualified Issue: %s", issue.Project.Name)
	message := fmt.Sprintf("%s\nScore: %.2f | %s", issue.Title, issue.QualifiedScore.TotalScore, issue.Type)

	n.logToNotificationsFile(issue.Title, issue.URL, issue.QualifiedScore.TotalScore, "Desktop")

	_, err := fmt.Printf("\n🔔 DESKTOP NOTIFICATION\n%s\n%s\n", title, message)
	return err
}
