package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	GitHubToken        string
	TelegramBotToken   string
	TelegramChatID     int64
	CheckInterval      int
	MaxIssuesPerRepo   int
	MaxProjects        int
	DBConnectionString string
	LogLevel           string
	LogFormat          string
	Email              *EmailConfig
	AntiSpam           *NotificationSpamConfig
	DigestMode         bool
	DigestTime         string
	Assignment         *AssignmentConfig
	Scoring            *ScoringConfig
	Display            *DisplayConfig
	Qualified          *QualifiedIssueConfig
	Notification       *NotificationConfig
	MCP                *MCPConfig
}

type MCPConfig struct {
	Server        *MCPServerConfig
	Client        *MCPClientConfig
	AIEnhancement *AIEnhancementConfig
}

type MCPServerConfig struct {
	Enabled   bool
	Transport string
	HTTPPort  int
	HTTPHost  string
}

type MCPClientConfig struct {
	Enabled bool
	Servers []MCPServerDefinition
}

type MCPServerDefinition struct {
	Name     string
	Command  string
	Args     []string
	Env      map[string]string
	Endpoint string
	APIKey   string
}

type AIEnhancementConfig struct {
	Enabled           bool
	EnhanceComments   bool
	SuggestSolutions  bool
	AnalyzeDifficulty bool
	Provider          string
}

type QualifiedIssueConfig struct {
	MinScore        float64
	Types           []string
	ExcludeLabels   []string
	IncludeLabels   []string
	MinStars        int
	RequireApproval bool
}

type NotificationConfig struct {
	LocalEnabled      bool
	EmailEnabled      bool
	EmailMinScore     float64
	MaxPerHour        int
	MaxPerDay         int
	DigestMode        bool
	DigestInterval    time.Duration
	NeverNotifyTwice  bool
	CheckUserComments bool
	CheckUserPRs      bool
}

type ScoringConfig struct {
	StarWeight               float64
	CommentWeight            float64
	RecencyWeight            float64
	LabelWeight              float64
	DifficultyWeight         float64
	DescriptionQualityWeight float64
	ActivityWeight           float64
	MaintainerWeight         float64
	ContributorFriendlyBonus float64
	WeekendBonus             float64
	MaxScore                 float64
}

type DisplayConfig struct {
	Mode               string
	MaxGoodFirstIssues int
	MaxOtherIssues     int
	MaxAssignedIssues  int
	ShowScoreBreakdown bool
}

type AssignmentConfig struct {
	Enabled          bool
	AutoMode         bool
	MaxDaily         int
	CooldownMins     int
	CheckEligibility bool
	AutoComment      bool
}

type EmailConfig struct {
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	FromEmail    string
	ToEmail      string
	Mode         string
	MaxPerHour   int
	MaxPerDay    int
	Verified     bool
}

type ConfigValidationError struct {
	Field   string
	Message string
}

func (e ConfigValidationError) Error() string {
	return fmt.Sprintf("config validation error: %s - %s", e.Field, e.Message)
}

func LoadConfig() (*Config, error) {
	config := &Config{
		GitHubToken:        strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		TelegramBotToken:   strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		CheckInterval:      3600,
		MaxIssuesPerRepo:   10,
		MaxProjects:        50,
		LogLevel:           "info",
		LogFormat:          "text",
		DBConnectionString: os.Getenv("DB_CONNECTION_STRING"),
	}

	if chatEnv := os.Getenv("TELEGRAM_CHAT_ID"); chatEnv != "" {
		parsed, err := strconv.ParseInt(chatEnv, 10, 64)
		if err != nil {
			return nil, ConfigValidationError{Field: "TELEGRAM_CHAT_ID", Message: fmt.Sprintf("invalid value %q: %v", chatEnv, err)}
		}
		config.TelegramChatID = parsed
	} else {
		config.TelegramChatID = 683539779
	}

	if intervalEnv := os.Getenv("CHECK_INTERVAL"); intervalEnv != "" {
		parsed, err := strconv.Atoi(intervalEnv)
		if err != nil {
			return nil, ConfigValidationError{Field: "CHECK_INTERVAL", Message: fmt.Sprintf("invalid value %q: %v", intervalEnv, err)}
		}
		if parsed <= 0 {
			return nil, ConfigValidationError{Field: "CHECK_INTERVAL", Message: "must be positive"}
		}
		config.CheckInterval = parsed
	}

	if maxEnv := os.Getenv("MAX_ISSUES_PER_REPO"); maxEnv != "" {
		parsed, err := strconv.Atoi(maxEnv)
		if err != nil {
			return nil, ConfigValidationError{Field: "MAX_ISSUES_PER_REPO", Message: fmt.Sprintf("invalid value %q: %v", maxEnv, err)}
		}
		if parsed <= 0 {
			return nil, ConfigValidationError{Field: "MAX_ISSUES_PER_REPO", Message: "must be positive"}
		}
		config.MaxIssuesPerRepo = parsed
	}

	if maxProjEnv := os.Getenv("MAX_PROJECTS"); maxProjEnv != "" {
		parsed, err := strconv.Atoi(maxProjEnv)
		if err != nil {
			return nil, ConfigValidationError{Field: "MAX_PROJECTS", Message: fmt.Sprintf("invalid value %q: %v", maxProjEnv, err)}
		}
		if parsed <= 0 {
			return nil, ConfigValidationError{Field: "MAX_PROJECTS", Message: "must be positive"}
		}
		config.MaxProjects = parsed
	}

	if dbConn := os.Getenv("DB_CONNECTION_STRING"); dbConn != "" {
		config.DBConnectionString = dbConn
	} else {
		config.DBConnectionString = "host=localhost user=postgres password=postgres dbname=issue_finder sslmode=disable port=5432"
	}

	if level := os.Getenv("LOG_LEVEL"); level != "" {
		validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
		if !validLevels[strings.ToLower(level)] {
			return nil, ConfigValidationError{Field: "LOG_LEVEL", Message: fmt.Sprintf("invalid level %q, must be one of: debug, info, warn, error", level)}
		}
		config.LogLevel = strings.ToLower(level)
	}

	if format := os.Getenv("LOG_FORMAT"); format != "" {
		validFormats := map[string]bool{"text": true, "json": true}
		if !validFormats[strings.ToLower(format)] {
			return nil, ConfigValidationError{Field: "LOG_FORMAT", Message: fmt.Sprintf("invalid format %q, must be one of: text, json", format)}
		}
		config.LogFormat = strings.ToLower(format)
	}

	config.Email = loadEmailConfigFromEnv()

	config.AntiSpam = loadAntiSpamConfigFromEnv()

	config.Assignment = loadAssignmentConfigFromEnv()

	config.Scoring = loadScoringConfigFromEnv()

	config.Display = loadDisplayConfigFromEnv()

	config.Qualified = loadQualifiedIssueConfigFromEnv()

	config.Notification = loadNotificationConfigFromEnv()

	config.MCP = LoadMCPConfig()

	if digestMode := os.Getenv("DIGEST_MODE"); digestMode == "true" {
		config.DigestMode = true
	}
	if digestTime := os.Getenv("DIGEST_TIME"); digestTime != "" {
		config.DigestTime = digestTime
	} else {
		config.DigestTime = "09:00"
	}

	return config, nil
}

func (c *Config) Validate() error {
	if c.GitHubToken == "" {
		return ConfigValidationError{Field: "GITHUB_TOKEN", Message: "is required"}
	}

	if len(c.GitHubToken) < 10 {
		return ConfigValidationError{Field: "GITHUB_TOKEN", Message: "appears to be invalid (too short)"}
	}

	if c.CheckInterval < 60 {
		return ConfigValidationError{Field: "CHECK_INTERVAL", Message: "must be at least 60 seconds to avoid rate limiting"}
	}

	if c.MaxIssuesPerRepo > 100 {
		return ConfigValidationError{Field: "MAX_ISSUES_PER_REPO", Message: "cannot exceed 100"}
	}

	if c.MaxProjects > 200 {
		return ConfigValidationError{Field: "MAX_PROJECTS", Message: "cannot exceed 200"}
	}

	if err := validateMCPConfig(c.MCP); err != nil {
		return err
	}

	return nil
}

func (c *Config) IsTelegramEnabled() bool {
	return c.TelegramBotToken != ""
}

func (c *Config) IsEmailEnabled() bool {
	return c.Email != nil && c.Email.SMTPHost != ""
}

func (c *Config) GetMCPServerConfig() *MCPServerConfig {
	if c.MCP == nil || c.MCP.Server == nil {
		return &MCPServerConfig{
			Enabled:   false,
			Transport: "stdio",
			HTTPPort:  8080,
			HTTPHost:  "localhost",
		}
	}
	return c.MCP.Server
}

func (c *Config) GetMCPClientConfig() *MCPClientConfig {
	if c.MCP == nil || c.MCP.Client == nil {
		return &MCPClientConfig{
			Enabled: false,
			Servers: []MCPServerDefinition{},
		}
	}
	return c.MCP.Client
}

func LoadMCPConfig() *MCPConfig {
	config := &MCPConfig{
		Server: &MCPServerConfig{
			Enabled:   false,
			Transport: "stdio",
			HTTPPort:  8080,
			HTTPHost:  "localhost",
		},
		Client: &MCPClientConfig{
			Enabled: false,
			Servers: []MCPServerDefinition{},
		},
		AIEnhancement: &AIEnhancementConfig{
			Enabled:           false,
			EnhanceComments:   true,
			SuggestSolutions:  true,
			AnalyzeDifficulty: true,
			Provider:          "claude",
		},
	}

	if enabled := os.Getenv("MCP_SERVER_ENABLED"); enabled == "true" {
		config.Server.Enabled = true
	}

	if transport := os.Getenv("MCP_TRANSPORT"); transport != "" {
		validTransports := map[string]bool{"stdio": true, "http": true, "sse": true}
		if validTransports[strings.ToLower(transport)] {
			config.Server.Transport = strings.ToLower(transport)
		}
	}

	if port := os.Getenv("MCP_HTTP_PORT"); port != "" {
		if val, err := strconv.Atoi(port); err == nil && val > 0 && val < 65536 {
			config.Server.HTTPPort = val
		}
	}

	if host := os.Getenv("MCP_HTTP_HOST"); host != "" {
		config.Server.HTTPHost = host
	}

	if enabled := os.Getenv("MCP_CLIENT_ENABLED"); enabled == "true" {
		config.Client.Enabled = true
	}

	if provider := os.Getenv("MCP_AI_PROVIDER"); provider != "" {
		validProviders := map[string]bool{"claude": true, "openai": true, "local": true}
		if validProviders[strings.ToLower(provider)] {
			config.AIEnhancement.Provider = strings.ToLower(provider)
		}
	}

	if enabled := os.Getenv("MCP_AI_ENHANCEMENT_ENABLED"); enabled == "true" {
		config.AIEnhancement.Enabled = true
	}

	if enhance := os.Getenv("MCP_ENHANCE_COMMENTS"); enhance == "false" {
		config.AIEnhancement.EnhanceComments = false
	}

	if suggest := os.Getenv("MCP_SUGGEST_SOLUTIONS"); suggest == "false" {
		config.AIEnhancement.SuggestSolutions = false
	}

	if analyze := os.Getenv("MCP_ANALYZE_DIFFICULTY"); analyze == "false" {
		config.AIEnhancement.AnalyzeDifficulty = false
	}

	return config
}

func validateMCPConfig(config *MCPConfig) error {
	if config == nil {
		return nil
	}

	if config.Server != nil && config.Server.Enabled {
		validTransports := map[string]bool{"stdio": true, "http": true, "sse": true}
		if !validTransports[config.Server.Transport] {
			return ConfigValidationError{
				Field:   "MCP_TRANSPORT",
				Message: fmt.Sprintf("invalid transport %q, must be one of: stdio, http, sse", config.Server.Transport),
			}
		}

		if config.Server.HTTPPort <= 0 || config.Server.HTTPPort > 65535 {
			return ConfigValidationError{
				Field:   "MCP_HTTP_PORT",
				Message: "must be between 1 and 65535",
			}
		}

		if config.Server.HTTPHost == "" {
			return ConfigValidationError{
				Field:   "MCP_HTTP_HOST",
				Message: "cannot be empty when MCP server is enabled",
			}
		}
	}

	if config.AIEnhancement != nil && config.AIEnhancement.Enabled {
		validProviders := map[string]bool{"claude": true, "openai": true, "local": true}
		if !validProviders[config.AIEnhancement.Provider] {
			return ConfigValidationError{
				Field:   "MCP_AI_PROVIDER",
				Message: fmt.Sprintf("invalid provider %q, must be one of: claude, openai, local", config.AIEnhancement.Provider),
			}
		}
	}

	return nil
}

func loadAntiSpamConfigFromEnv() *NotificationSpamConfig {
	config := DefaultNotificationSpamConfig()

	if maxHourly := os.Getenv("MAX_NOTIFICATIONS_PER_HOUR"); maxHourly != "" {
		if val, err := strconv.Atoi(maxHourly); err == nil && val > 0 {
			config.MaxNotificationsPerHour = val
		}
	}

	if maxDaily := os.Getenv("DAILY_NOTIFICATION_LIMIT"); maxDaily != "" {
		if val, err := strconv.Atoi(maxDaily); err == nil && val > 0 {
			config.DailyNotificationLimit = val
		}
	}

	if maxPerProject := os.Getenv("MAX_NOTIFICATIONS_PER_PROJECT"); maxPerProject != "" {
		if val, err := strconv.Atoi(maxPerProject); err == nil && val > 0 {
			config.MaxNotificationsPerProject = val
		}
	}

	if cooldownHours := os.Getenv("NOTIFICATION_COOLDOWN_HOURS"); cooldownHours != "" {
		if val, err := strconv.Atoi(cooldownHours); err == nil && val > 0 {
			config.NotificationCooldownPeriod = time.Duration(val) * time.Hour
		}
	}

	if digestMode := os.Getenv("DIGEST_MODE"); digestMode == "true" {
		config.EnableDigestMode = true
	}

	if digestTime := os.Getenv("DIGEST_TIME"); digestTime != "" {
		config.DigestTime = digestTime
	}

	if checkOpen := os.Getenv("CHECK_ISSUE_OPEN_BEFORE_NOTIFY"); checkOpen == "false" {
		config.CheckIssueOpenBeforeNotify = false
	}

	return &config
}

func loadAssignmentConfigFromEnv() *AssignmentConfig {
	config := &AssignmentConfig{
		Enabled:      false,
		AutoMode:     false,
		MaxDaily:     5,
		CooldownMins: 30,
	}

	if enabled := os.Getenv("ASSIGNMENT_ENABLED"); enabled == "true" {
		config.Enabled = true
	}

	if autoMode := os.Getenv("ASSIGNMENT_AUTO_MODE"); autoMode == "true" {
		config.AutoMode = true
	}

	if maxDaily := os.Getenv("ASSIGNMENT_MAX_DAILY"); maxDaily != "" {
		if val, err := strconv.Atoi(maxDaily); err == nil && val > 0 {
			config.MaxDaily = val
		}
	}

	if cooldown := os.Getenv("ASSIGNMENT_COOLDOWN_MINS"); cooldown != "" {
		if val, err := strconv.Atoi(cooldown); err == nil && val > 0 {
			config.CooldownMins = val
		}
	}

	return config
}

func loadEmailConfigFromEnv() *EmailConfig {
	config := &EmailConfig{
		SMTPHost:     strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:     strings.TrimSpace(os.Getenv("SMTP_PORT")),
		SMTPUsername: strings.TrimSpace(os.Getenv("SMTP_USERNAME")),
		SMTPPassword: strings.TrimSpace(os.Getenv("SMTP_PASSWORD")),
		FromEmail:    strings.TrimSpace(os.Getenv("FROM_EMAIL")),
		ToEmail:      strings.TrimSpace(os.Getenv("TO_EMAIL")),
		Mode:         strings.TrimSpace(os.Getenv("EMAIL_MODE")),
		MaxPerHour:   10,
		MaxPerDay:    50,
	}

	if config.SMTPPort == "" {
		config.SMTPPort = "587"
	}

	if config.Mode == "" {
		config.Mode = "instant"
	}

	if maxPerHour := os.Getenv("MAX_EMAILS_PER_HOUR"); maxPerHour != "" {
		if val, err := strconv.Atoi(maxPerHour); err == nil && val > 0 {
			config.MaxPerHour = val
		}
	}

	if maxPerDay := os.Getenv("MAX_EMAILS_PER_DAY"); maxPerDay != "" {
		if val, err := strconv.Atoi(maxPerDay); err == nil && val > 0 {
			config.MaxPerDay = val
		}
	}

	if config.SMTPHost == "" {
		return nil
	}

	return config
}

func loadScoringConfigFromEnv() *ScoringConfig {
	config := &ScoringConfig{
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

	if weight := os.Getenv("SCORING_STAR_WEIGHT"); weight != "" {
		if val, err := strconv.ParseFloat(weight, 64); err == nil && val >= 0 {
			config.StarWeight = val
		}
	}

	if weight := os.Getenv("SCORING_COMMENT_WEIGHT"); weight != "" {
		if val, err := strconv.ParseFloat(weight, 64); err == nil && val >= 0 {
			config.CommentWeight = val
		}
	}

	if weight := os.Getenv("SCORING_RECENCY_WEIGHT"); weight != "" {
		if val, err := strconv.ParseFloat(weight, 64); err == nil && val >= 0 {
			config.RecencyWeight = val
		}
	}

	if weight := os.Getenv("SCORING_LABEL_WEIGHT"); weight != "" {
		if val, err := strconv.ParseFloat(weight, 64); err == nil && val >= 0 {
			config.LabelWeight = val
		}
	}

	if weight := os.Getenv("SCORING_DESCRIPTION_WEIGHT"); weight != "" {
		if val, err := strconv.ParseFloat(weight, 64); err == nil && val >= 0 {
			config.DescriptionQualityWeight = val
		}
	}

	if weight := os.Getenv("SCORING_ACTIVITY_WEIGHT"); weight != "" {
		if val, err := strconv.ParseFloat(weight, 64); err == nil && val >= 0 {
			config.ActivityWeight = val
		}
	}

	if weight := os.Getenv("SCORING_MAINTAINER_WEIGHT"); weight != "" {
		if val, err := strconv.ParseFloat(weight, 64); err == nil && val >= 0 {
			config.MaintainerWeight = val
		}
	}

	if weight := os.Getenv("SCORING_CONTRIBUTOR_FRIENDLY_BONUS"); weight != "" {
		if val, err := strconv.ParseFloat(weight, 64); err == nil && val >= 0 {
			config.ContributorFriendlyBonus = val
		}
	}

	if max := os.Getenv("SCORING_MAX_SCORE"); max != "" {
		if val, err := strconv.ParseFloat(max, 64); err == nil && val > 0 {
			config.MaxScore = val
		}
	}

	return config
}

func loadDisplayConfigFromEnv() *DisplayConfig {
	config := &DisplayConfig{
		Mode:               "partitioned",
		MaxGoodFirstIssues: 15,
		MaxOtherIssues:     10,
		MaxAssignedIssues:  10,
		ShowScoreBreakdown: true,
	}

	if mode := os.Getenv("DISPLAY_MODE"); mode != "" {
		validModes := map[string]bool{"partitioned": true, "simple": true, "json": true}
		if validModes[strings.ToLower(mode)] {
			config.Mode = strings.ToLower(mode)
		}
	}

	if max := os.Getenv("DISPLAY_MAX_GOOD_FIRST"); max != "" {
		if val, err := strconv.Atoi(max); err == nil && val > 0 {
			config.MaxGoodFirstIssues = val
		}
	}

	if max := os.Getenv("DISPLAY_MAX_OTHER"); max != "" {
		if val, err := strconv.Atoi(max); err == nil && val > 0 {
			config.MaxOtherIssues = val
		}
	}

	if show := os.Getenv("DISPLAY_SHOW_SCORE_BREAKDOWN"); show == "false" {
		config.ShowScoreBreakdown = false
	}

	return config
}

func loadQualifiedIssueConfigFromEnv() *QualifiedIssueConfig {
	config := &QualifiedIssueConfig{
		MinScore:        0.6,
		Types:           []string{"bug", "feature", "enhancement"},
		ExcludeLabels:   []string{"question", "support", "wontfix", "duplicate", "invalid"},
		IncludeLabels:   []string{"confirmed", "triage/accepted", "approved", "help wanted"},
		MinStars:        100,
		RequireApproval: false,
	}

	if minScore := os.Getenv("QUALIFIED_MIN_SCORE"); minScore != "" {
		if val, err := strconv.ParseFloat(minScore, 64); err == nil && val >= 0 && val <= 1 {
			config.MinScore = val
		}
	}

	if types := os.Getenv("QUALIFIED_TYPES"); types != "" {
		config.Types = strings.Split(types, ",")
	}

	if excludeLabels := os.Getenv("QUALIFIED_EXCLUDE_LABELS"); excludeLabels != "" {
		config.ExcludeLabels = strings.Split(excludeLabels, ",")
	}

	if includeLabels := os.Getenv("QUALIFIED_INCLUDE_LABELS"); includeLabels != "" {
		config.IncludeLabels = strings.Split(includeLabels, ",")
	}

	if minStars := os.Getenv("QUALIFIED_MIN_STARS"); minStars != "" {
		if val, err := strconv.Atoi(minStars); err == nil && val >= 0 {
			config.MinStars = val
		}
	}

	if requireApproval := os.Getenv("QUALIFIED_REQUIRE_APPROVAL"); requireApproval == "true" {
		config.RequireApproval = true
	}

	return config
}

func loadNotificationConfigFromEnv() *NotificationConfig {
	config := &NotificationConfig{
		LocalEnabled:      true,
		EmailEnabled:      false,
		EmailMinScore:     0.7,
		MaxPerHour:        5,
		MaxPerDay:         20,
		DigestMode:        false,
		DigestInterval:    6 * time.Hour,
		NeverNotifyTwice:  true,
		CheckUserComments: true,
		CheckUserPRs:      true,
	}

	if localEnabled := os.Getenv("NOTIFY_LOCAL"); localEnabled == "false" {
		config.LocalEnabled = false
	}

	if emailEnabled := os.Getenv("NOTIFY_EMAIL"); emailEnabled == "true" {
		config.EmailEnabled = true
	}

	if emailMinScore := os.Getenv("NOTIFY_EMAIL_MIN_SCORE"); emailMinScore != "" {
		if val, err := strconv.ParseFloat(emailMinScore, 64); err == nil && val >= 0 && val <= 1 {
			config.EmailMinScore = val
		}
	}

	if maxPerHour := os.Getenv("NOTIFY_MAX_PER_HOUR"); maxPerHour != "" {
		if val, err := strconv.Atoi(maxPerHour); err == nil && val > 0 {
			config.MaxPerHour = val
		}
	}

	if maxPerDay := os.Getenv("NOTIFY_MAX_PER_DAY"); maxPerDay != "" {
		if val, err := strconv.Atoi(maxPerDay); err == nil && val > 0 {
			config.MaxPerDay = val
		}
	}

	if digestMode := os.Getenv("NOTIFY_DIGEST_MODE"); digestMode == "true" {
		config.DigestMode = true
	}

	if digestInterval := os.Getenv("NOTIFY_DIGEST_INTERVAL"); digestInterval != "" {
		if val, err := time.ParseDuration(digestInterval); err == nil {
			config.DigestInterval = val
		}
	}

	if neverTwice := os.Getenv("NEVER_NOTIFY_TWICE"); neverTwice == "false" {
		config.NeverNotifyTwice = false
	}

	if checkComments := os.Getenv("CHECK_USER_COMMENTS"); checkComments == "false" {
		config.CheckUserComments = false
	}

	if checkPRs := os.Getenv("CHECK_USER_PRS"); checkPRs == "false" {
		config.CheckUserPRs = false
	}

	return config
}
