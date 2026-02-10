package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level represents log level
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Logger provides structured logging to file
type Logger struct {
	mu       sync.Mutex
	file     *os.File
	logger   *log.Logger
	level    Level
	filePath string
}

var (
	defaultLogger *Logger
	once          sync.Once
)

// Init initializes the default logger with the given data path
func Init(dataPath string) error {
	var initErr error
	once.Do(func() {
		logDir := filepath.Join(dataPath, "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			initErr = fmt.Errorf("failed to create log directory: %w", err)
			return
		}

		// Create log file with date in name
		logFileName := fmt.Sprintf("aagent_%s.log", time.Now().Format("2006-01-02"))
		logPath := filepath.Join(logDir, logFileName)

		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			initErr = fmt.Errorf("failed to open log file: %w", err)
			return
		}

		defaultLogger = &Logger{
			file:     file,
			logger:   log.New(file, "", 0),
			level:    LevelDebug,
			filePath: logPath,
		}
	})
	return initErr
}

// Close closes the log file
func Close() {
	if defaultLogger != nil && defaultLogger.file != nil {
		defaultLogger.file.Close()
	}
}

// GetLogPath returns the current log file path
func GetLogPath() string {
	if defaultLogger != nil {
		return defaultLogger.filePath
	}
	return ""
}

// SetLevel sets the minimum log level
func SetLevel(level Level) {
	if defaultLogger != nil {
		defaultLogger.mu.Lock()
		defaultLogger.level = level
		defaultLogger.mu.Unlock()
	}
}

// Writer returns an io.Writer that writes to the log file
func Writer() io.Writer {
	if defaultLogger != nil {
		return defaultLogger.file
	}
	return io.Discard
}

func logf(level Level, format string, args ...interface{}) {
	if defaultLogger == nil {
		return
	}

	defaultLogger.mu.Lock()
	defer defaultLogger.mu.Unlock()

	if level < defaultLogger.level {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	defaultLogger.logger.Printf("[%s] [%s] %s", timestamp, level.String(), msg)
}

// Debug logs a debug message
func Debug(format string, args ...interface{}) {
	logf(LevelDebug, format, args...)
}

// Info logs an info message
func Info(format string, args ...interface{}) {
	logf(LevelInfo, format, args...)
}

// Warn logs a warning message
func Warn(format string, args ...interface{}) {
	logf(LevelWarn, format, args...)
}

// Error logs an error message
func Error(format string, args ...interface{}) {
	logf(LevelError, format, args...)
}

// LogRequest logs an LLM request
func LogRequest(model string, messageCount int, hasTools bool) {
	Info("LLM Request: model=%s messages=%d tools=%v", model, messageCount, hasTools)
}

// LogRequestWithContent logs an LLM request with the last message content
func LogRequestWithContent(model string, messageCount int, hasTools bool, lastMessage string) {
	// Truncate long messages
	if len(lastMessage) > 200 {
		lastMessage = lastMessage[:200] + "..."
	}
	// Replace newlines for single-line logging
	lastMessage = strings.ReplaceAll(lastMessage, "\n", "\\n")
	Info("LLM Request: model=%s messages=%d tools=%v last_msg=\"%s\"", model, messageCount, hasTools, lastMessage)
}

// LogResponse logs an LLM response
func LogResponse(inputTokens, outputTokens int, toolCallCount int, err error) {
	if err != nil {
		Error("LLM Response: error=%v", err)
	} else {
		Info("LLM Response: input_tokens=%d output_tokens=%d tool_calls=%d", inputTokens, outputTokens, toolCallCount)
	}
}

// LogResponseWithContent logs an LLM response with content preview
func LogResponseWithContent(inputTokens, outputTokens int, toolCallCount int, content string, toolNames []string) {
	// Truncate long content
	if len(content) > 300 {
		content = content[:300] + "..."
	}
	content = strings.ReplaceAll(content, "\n", "\\n")

	if len(toolNames) > 0 {
		Info("LLM Response: tokens=%d/%d tools=[%s] content=\"%s\"",
			inputTokens, outputTokens, strings.Join(toolNames, ","), content)
	} else {
		Info("LLM Response: tokens=%d/%d content=\"%s\"", inputTokens, outputTokens, content)
	}
}

// LogToolExecution logs tool execution
func LogToolExecution(toolName string, success bool, duration time.Duration) {
	if success {
		Debug("Tool executed: name=%s duration=%v", toolName, duration)
	} else {
		Warn("Tool failed: name=%s duration=%v", toolName, duration)
	}
}

// LogSession logs session events
func LogSession(event string, sessionID string, details string) {
	Info("Session %s: id=%s %s", event, sessionID, details)
}
