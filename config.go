package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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

	return nil
}

func (c *Config) IsTelegramEnabled() bool {
	return c.TelegramBotToken != ""
}

func (c *Config) IsEmailEnabled() bool {
	return c.Email != nil && c.Email.SMTPHost != ""
}
