package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

type EmailSender struct {
	config      *EmailConfig
	rateLimiter *EmailRateLimiter
	retryPolicy *EmailRetryPolicy
	mu          sync.RWMutex
	sentEmails  map[string]time.Time
	verified    bool
}

type EmailRateLimiter struct {
	mu         sync.Mutex
	count      int
	maxPerHour int
	lastReset  time.Time
	dailyCount int
	maxPerDay  int
	dayReset   time.Time
}

type EmailRetryPolicy struct {
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

type EmailType string

const (
	EmailTypeNewIssue       EmailType = "new_issue"
	EmailTypeDailyDigest    EmailType = "daily_digest"
	EmailTypeAssignmentConf EmailType = "assignment_confirmation"
	EmailTypeAssignmentReq  EmailType = "assignment_request"
)

func NewEmailSender(config *EmailConfig, maxPerHour, maxPerDay int) *EmailSender {
	return &EmailSender{
		config: config,
		rateLimiter: &EmailRateLimiter{
			maxPerHour: maxPerHour,
			maxPerDay:  maxPerDay,
			lastReset:  time.Now().Truncate(time.Hour),
			dayReset:   time.Now().Truncate(24 * time.Hour),
		},
		retryPolicy: &EmailRetryPolicy{
			maxRetries:     3,
			initialBackoff: 5 * time.Second,
			maxBackoff:     60 * time.Second,
		},
		sentEmails: make(map[string]time.Time),
	}
}

func (s *EmailSender) VerifyConfig() error {
	if s.config == nil {
		return fmt.Errorf("email config is nil")
	}
	if s.config.SMTPHost == "" {
		return fmt.Errorf("SMTP host is required")
	}
	if s.config.SMTPUsername == "" {
		return fmt.Errorf("SMTP username is required")
	}
	if s.config.SMTPPassword == "" {
		return fmt.Errorf("SMTP password is required")
	}
	if s.config.FromEmail == "" {
		return fmt.Errorf("from email is required")
	}
	if s.config.ToEmail == "" {
		return fmt.Errorf("to email is required")
	}

	host := s.config.SMTPHost
	port := s.config.SMTPPort
	if port == "" {
		port = "587"
	}

	addr := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to SMTP server %s: %w", addr, err)
	}
	conn.Close()

	s.verified = true
	return nil
}

func (s *EmailSender) CanSend(emailType EmailType, issueURL string) (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if sentTime, exists := s.sentEmails[issueURL+"_"+string(emailType)]; exists {
		if time.Since(sentTime) < 24*time.Hour {
			return false, fmt.Sprintf("already sent %s email in last 24h", emailType)
		}
	}

	if !s.rateLimiter.CanSend() {
		return false, "email rate limit exceeded"
	}

	return true, ""
}

func (s *EmailSender) RecordSent(emailType EmailType, issueURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := issueURL + "_" + string(emailType)
	s.sentEmails[key] = time.Now()
	s.rateLimiter.Increment()

	cutoff := time.Now().Add(-48 * time.Hour)
	for k, v := range s.sentEmails {
		if v.Before(cutoff) {
			delete(s.sentEmails, k)
		}
	}
}

func (r *EmailRateLimiter) CanSend() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	currentHour := now.Truncate(time.Hour)
	currentDay := now.Truncate(24 * time.Hour)

	if !r.lastReset.Equal(currentHour) {
		r.count = 0
		r.lastReset = currentHour
	}

	if !r.dayReset.Equal(currentDay) {
		r.dailyCount = 0
		r.dayReset = currentDay
	}

	if r.maxPerHour > 0 && r.count >= r.maxPerHour {
		return false
	}

	if r.maxPerDay > 0 && r.dailyCount >= r.maxPerDay {
		return false
	}

	return true
}

func (r *EmailRateLimiter) Increment() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.count++
	r.dailyCount++
}

func (s *EmailSender) SendEmail(subject, htmlBody, textBody string) error {
	if !s.verified {
		if err := s.VerifyConfig(); err != nil {
			return fmt.Errorf("email not verified: %w", err)
		}
	}

	host := s.config.SMTPHost
	port := s.config.SMTPPort
	if port == "" {
		port = "587"
	}

	if strings.Contains(host, ":") {
		cleanHost, existingPort, err := net.SplitHostPort(host)
		if err == nil {
			host = cleanHost
			if port == "" {
				port = existingPort
			}
		}
	}

	var lastErr error
	for attempt := 0; attempt < s.retryPolicy.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := s.retryPolicy.initialBackoff * time.Duration(1<<uint(attempt-1))
			if backoff > s.retryPolicy.maxBackoff {
				backoff = s.retryPolicy.maxBackoff
			}
			log.Printf("[Email] Retry %d/%d after %v", attempt+1, s.retryPolicy.maxRetries, backoff)
			time.Sleep(backoff)
		}

		err := s.trySendEmail(host, port, subject, htmlBody, textBody)
		if err == nil {
			log.Printf("[Email] Successfully sent email: %s", subject)
			return nil
		}

		lastErr = err
		log.Printf("[Email] Attempt %d failed: %v", attempt+1, err)
	}

	return fmt.Errorf("failed after %d retries: %w", s.retryPolicy.maxRetries, lastErr)
}

func (s *EmailSender) trySendEmail(host, port, subject, htmlBody, textBody string) error {
	addr := net.JoinHostPort(host, port)

	var auth smtp.Auth
	if s.config.SMTPUsername != "" && s.config.SMTPPassword != "" {
		auth = smtp.PlainAuth("", s.config.SMTPUsername, s.config.SMTPPassword, host)
	}

	msg := s.buildMessage(subject, htmlBody, textBody)

	if port == "465" {
		return s.sendWithTLS(addr, auth, msg)
	}

	return smtp.SendMail(addr, auth, s.config.FromEmail, []string{s.config.ToEmail}, msg)
}

func (s *EmailSender) sendWithTLS(addr string, auth smtp.Auth, msg []byte) error {
	host, _, _ := net.SplitHostPort(addr)

	tlsConfig := &tls.Config{
		InsecureSkipVerify: false,
		ServerName:         host,
	}

	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("TLS dial failed: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP client creation failed: %w", err)
	}
	defer client.Close()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth failed: %w", err)
		}
	}

	if err := client.Mail(s.config.FromEmail); err != nil {
		return fmt.Errorf("MAIL FROM failed: %w", err)
	}

	if err := client.Rcpt(s.config.ToEmail); err != nil {
		return fmt.Errorf("RCPT TO failed: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA command failed: %w", err)
	}

	_, err = w.Write(msg)
	if err != nil {
		return fmt.Errorf("writing message failed: %w", err)
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("closing data writer failed: %w", err)
	}

	return client.Quit()
}

func (s *EmailSender) buildMessage(subject, htmlBody, textBody string) []byte {
	var msg strings.Builder

	msg.WriteString(fmt.Sprintf("From: %s\r\n", s.config.FromEmail))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", s.config.ToEmail))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(htmlBody)

	if textBody != "" && htmlBody == "" {
		msg.Reset()
		msg.WriteString(fmt.Sprintf("From: %s\r\n", s.config.FromEmail))
		msg.WriteString(fmt.Sprintf("To: %s\r\n", s.config.ToEmail))
		msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
		msg.WriteString("MIME-Version: 1.0\r\n")
		msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
		msg.WriteString("\r\n")
		msg.WriteString(textBody)
	}

	return []byte(msg.String())
}

func (s *EmailSender) SendNewIssueEmail(issue Issue, scoreBreakdown *ScoreBreakdown) error {
	canSend, reason := s.CanSend(EmailTypeNewIssue, issue.URL)
	if !canSend {
		return fmt.Errorf("cannot send: %s", reason)
	}

	template := NewIssueEmailTemplate(issue, scoreBreakdown)
	err := s.SendEmail(template.Subject, template.HTMLBody, template.TextBody)
	if err != nil {
		return err
	}

	s.RecordSent(EmailTypeNewIssue, issue.URL)
	return nil
}

func (s *EmailSender) SendDigestEmail(issues []Issue) error {
	canSend, reason := s.CanSend(EmailTypeDailyDigest, "digest_"+time.Now().Format("2006-01-02"))
	if !canSend {
		return fmt.Errorf("cannot send: %s", reason)
	}

	template := DigestEmailTemplate(issues)
	err := s.SendEmail(template.Subject, template.HTMLBody, template.TextBody)
	if err != nil {
		return err
	}

	s.RecordSent(EmailTypeDailyDigest, "digest_"+time.Now().Format("2006-01-02"))
	return nil
}

func (s *EmailSender) SendAssignmentConfirmationEmail(issue Issue) error {
	canSend, reason := s.CanSend(EmailTypeAssignmentConf, issue.URL)
	if !canSend {
		return fmt.Errorf("cannot send: %s", reason)
	}

	template := AssignmentConfirmationTemplate(issue)
	err := s.SendEmail(template.Subject, template.HTMLBody, template.TextBody)
	if err != nil {
		return err
	}

	s.RecordSent(EmailTypeAssignmentConf, issue.URL)
	return nil
}

func (s *EmailSender) SendAssignmentRequestEmail(issue Issue) error {
	canSend, reason := s.CanSend(EmailTypeAssignmentReq, issue.URL)
	if !canSend {
		return fmt.Errorf("cannot send: %s", reason)
	}

	template := AssignmentRequestTemplate(issue)
	err := s.SendEmail(template.Subject, template.HTMLBody, template.TextBody)
	if err != nil {
		return err
	}

	s.RecordSent(EmailTypeAssignmentReq, issue.URL)
	return nil
}

func (s *EmailSender) GetStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]interface{}{
		"verified":     s.verified,
		"sent_count":   len(s.sentEmails),
		"hourly_limit": s.rateLimiter.maxPerHour,
		"daily_limit":  s.rateLimiter.maxPerDay,
		"hourly_sent":  s.rateLimiter.count,
		"daily_sent":   s.rateLimiter.dailyCount,
	}
}
