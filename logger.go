package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarn
	LogLevelError
)

func (l LogLevel) String() string {
	switch l {
	case LogLevelDebug:
		return "DEBUG"
	case LogLevelInfo:
		return "INFO"
	case LogLevelWarn:
		return "WARN"
	case LogLevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

type Logger struct {
	mu      sync.Mutex
	level   LogLevel
	format  string
	output  io.Writer
	logFile *os.File
}

var (
	defaultLogger *Logger
	once          sync.Once
)

func InitLogger(level, format string) (*Logger, error) {
	var logLevel LogLevel
	switch strings.ToLower(level) {
	case "debug":
		logLevel = LogLevelDebug
	case "info":
		logLevel = LogLevelInfo
	case "warn":
		logLevel = LogLevelWarn
	case "error":
		logLevel = LogLevelError
	default:
		logLevel = LogLevelInfo
	}

	logger := &Logger{
		level:  logLevel,
		format: format,
		output: os.Stdout,
	}

	logDir := strings.TrimSpace(os.Getenv("LOG_DIR"))
	if logDir == "" {
		logDir = "logs"
	}

	if err := os.MkdirAll(logDir, 0755); err == nil {
		logFile, err := os.OpenFile(logDir+"/app.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			logger.logFile = logFile
			logger.output = io.MultiWriter(os.Stdout, logFile)
		}
	}

	once.Do(func() {
		defaultLogger = logger
	})

	return logger, nil
}

func GetLogger() *Logger {
	if defaultLogger == nil {
		defaultLogger, _ = InitLogger("info", "text")
	}
	return defaultLogger
}

func (l *Logger) log(level LogLevel, format string, args ...interface{}) {
	if level < l.level {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02T15:04:05.000Z07:00")

	if l.format == "json" {
		entry := map[string]interface{}{
			"timestamp": timestamp,
			"level":     level.String(),
			"message":   msg,
		}
		jsonBytes, _ := json.Marshal(entry)
		fmt.Fprintln(l.output, string(jsonBytes))
	} else {
		fmt.Fprintf(l.output, "[%s] [%s] %s\n", timestamp, level.String(), msg)
	}
}

func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(LogLevelDebug, format, args...)
}

func (l *Logger) Info(format string, args ...interface{}) {
	l.log(LogLevelInfo, format, args...)
}

func (l *Logger) Warn(format string, args ...interface{}) {
	l.log(LogLevelWarn, format, args...)
}

func (l *Logger) Error(format string, args ...interface{}) {
	l.log(LogLevelError, format, args...)
}

func (l *Logger) Fatal(format string, args ...interface{}) {
	l.log(LogLevelError, format, args...)
	os.Exit(1)
}

func (l *Logger) WithField(key string, value interface{}) *LogEntry {
	return &LogEntry{
		logger: l,
		fields: map[string]interface{}{key: value},
	}
}

func (l *Logger) WithFields(fields map[string]interface{}) *LogEntry {
	return &LogEntry{
		logger: l,
		fields: fields,
	}
}

func (l *Logger) Close() error {
	if l.logFile != nil {
		return l.logFile.Close()
	}
	return nil
}

type LogEntry struct {
	logger *Logger
	fields map[string]interface{}
}

func (e *LogEntry) log(level LogLevel, format string, args ...interface{}) {
	if level < e.logger.level {
		return
	}

	e.logger.mu.Lock()
	defer e.logger.mu.Unlock()

	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02T15:04:05.000Z07:00")

	if e.logger.format == "json" {
		entry := map[string]interface{}{
			"timestamp": timestamp,
			"level":     level.String(),
			"message":   msg,
		}
		for k, v := range e.fields {
			entry[k] = v
		}
		jsonBytes, _ := json.Marshal(entry)
		fmt.Fprintln(e.logger.output, string(jsonBytes))
	} else {
		fieldStr := ""
		for k, v := range e.fields {
			fieldStr += fmt.Sprintf(" %s=%v", k, v)
		}
		fmt.Fprintf(e.logger.output, "[%s] [%s]%s %s\n", timestamp, level.String(), fieldStr, msg)
	}
}

func (e *LogEntry) Debug(format string, args ...interface{}) {
	e.log(LogLevelDebug, format, args...)
}

func (e *LogEntry) Info(format string, args ...interface{}) {
	e.log(LogLevelInfo, format, args...)
}

func (e *LogEntry) Warn(format string, args ...interface{}) {
	e.log(LogLevelWarn, format, args...)
}

func (e *LogEntry) Error(format string, args ...interface{}) {
	e.log(LogLevelError, format, args...)
}

func Debug(format string, args ...interface{}) {
	GetLogger().Debug(format, args...)
}

func Info(format string, args ...interface{}) {
	GetLogger().Info(format, args...)
}

func Warn(format string, args ...interface{}) {
	GetLogger().Warn(format, args...)
}

func Error(format string, args ...interface{}) {
	GetLogger().Error(format, args...)
}

func Fatal(format string, args ...interface{}) {
	GetLogger().Fatal(format, args...)
}

func WithField(key string, value interface{}) *LogEntry {
	return GetLogger().WithField(key, value)
}

func WithFields(fields map[string]interface{}) *LogEntry {
	return GetLogger().WithFields(fields)
}

func init() {
	log.SetOutput(io.Discard)
}
