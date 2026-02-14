package main

import (
	"fmt"
	"log"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type EmailConfig struct {
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	FromEmail    string
	ToEmail      string
}

type LocalNotifier struct {
	emailConfig       *EmailConfig
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

	if n.emailConfig != nil && n.emailConfig.SMTPHost != "" {
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

	var body strings.Builder
	body.WriteString("Subject: New GitHub Issues Found\n\n")
	body.WriteString(fmt.Sprintf("Found %d new issues in Go DevOps projects:\n\n", len(issues)))

	for i, issue := range issues {
		if i >= 20 {
			body.WriteString(fmt.Sprintf("\n... and %d more issues\n", len(issues)-20))
			break
		}

		priority := "Medium"
		if issue.Score >= 0.8 {
			priority = "High"
		} else if issue.Score < 0.6 {
			priority = "Low"
		}

		body.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, priority, issue.Title))
		body.WriteString(fmt.Sprintf("   Project: %s/%s (%d stars)\n", issue.Project.Org, issue.Project.Name, issue.Project.Stars))
		body.WriteString(fmt.Sprintf("   Category: %s\n", issue.Project.Category))
		body.WriteString(fmt.Sprintf("   Score: %.2f | Comments: %d\n", issue.Score, issue.Comments))
		body.WriteString(fmt.Sprintf("   URL: %s\n", issue.URL))
		if len(issue.Labels) > 0 {
			body.WriteString(fmt.Sprintf("   Labels: %s\n", strings.Join(issue.Labels, ", ")))
		}
		body.WriteString("\n")
	}

	body.WriteString("\n---\n")
	body.WriteString("Scoring breakdown:\n")
	body.WriteString("- Stars: Project popularity\n")
	body.WriteString("- Comments: Low is better (less competition)\n")
	body.WriteString("- Recency: Recent issues get higher scores\n")
	body.WriteString("- Labels: Good first issue, help wanted, bug, enhancement help\n")
	body.WriteString("- Difficulty: Simpler issues get higher scores\n")

	if n.emailConfig.SMTPUsername == "" || n.emailConfig.SMTPPassword == "" ||
		n.emailConfig.FromEmail == "" || n.emailConfig.ToEmail == "" {
		return fmt.Errorf("incomplete SMTP configuration")
	}

	host := strings.TrimSpace(n.emailConfig.SMTPHost)
	port := strings.TrimSpace(n.emailConfig.SMTPPort)

	if host == "" {
		return fmt.Errorf("smtp host is not configured")
	}

	if strings.Contains(host, ":") {
		cleanHost, existingPort, err := net.SplitHostPort(host)
		if err != nil {
			return fmt.Errorf("invalid SMTP host %q: %w", host, err)
		}
		host = cleanHost
		if port == "" {
			port = existingPort
		}
	}

	if port == "" {
		port = "587"
	}

	auth := smtp.PlainAuth("", n.emailConfig.SMTPUsername, n.emailConfig.SMTPPassword, host)

	to := []string{n.emailConfig.ToEmail}
	msg := []byte(body.String())

	addr := net.JoinHostPort(host, port)
	if err := smtp.SendMail(addr, auth, n.emailConfig.FromEmail, to, msg); err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	n.logToFile("Email sent successfully")
	log.Printf("[Notifier] Email alert sent to %s via %s", n.emailConfig.ToEmail, addr)
	return nil
}

func (n *LocalNotifier) LogError(message string) {
	log.Printf("ERROR: %s", message)
	n.logToFile(fmt.Sprintf("ERROR: %s", message))
}

func (n *LocalNotifier) LogInfo(message string) {
	log.Printf("INFO: %s", message)
	n.logToFile(fmt.Sprintf("INFO: %s", message))
}
