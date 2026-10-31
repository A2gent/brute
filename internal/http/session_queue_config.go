package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Project setting holding named queues: JSON array of {"name": "godot", "limit": 1}.
const projectSessionQueuesKey = "session_queues"

const (
	sessionQueueNameMaxLength  = 40
	sessionQueueScopeSeparator = "/"
)

type sessionQueueConfig struct {
	Name  string `json:"name"`
	Limit int    `json:"limit"`
}

// parseSessionQueues returns nil (and no error) for an empty or malformed value so a
// broken setting degrades to the default queue instead of stalling every loop.
func parseSessionQueues(raw string) ([]sessionQueueConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var items []sessionQueueConfig
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("invalid session_queues JSON: %w", err)
	}
	queues := make([]sessionQueueConfig, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		name, err := validateSessionQueueName(item.Name)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, errors.New("duplicate session queue name: " + name)
		}
		seen[name] = struct{}{}
		queues = append(queues, sessionQueueConfig{Name: name, Limit: clampSessionQueueLimit(item.Limit)})
	}
	return queues, nil
}

func encodeSessionQueues(queues []sessionQueueConfig) string {
	if len(queues) == 0 {
		return ""
	}
	encoded, err := json.Marshal(queues)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func validateSessionQueueName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("session queue name is required")
	}
	if strings.Contains(name, sessionQueueScopeSeparator) {
		return "", errors.New("session queue name cannot contain " + strconv.Quote(sessionQueueScopeSeparator))
	}
	if len([]rune(name)) > sessionQueueNameMaxLength {
		return "", fmt.Errorf("session queue name is longer than %d characters", sessionQueueNameMaxLength)
	}
	return name, nil
}

func clampSessionQueueLimit(n int) int {
	if n < 1 {
		return 1
	}
	if n > maxParallelSessionsLimit {
		return maxParallelSessionsLimit
	}
	return n
}

// sessionQueueLimit returns the configured limit of a named queue.
func sessionQueueLimit(settings map[string]string, name string) (int, bool) {
	queues, err := parseSessionQueues(settings[projectSessionQueuesKey])
	if err != nil {
		return 0, false
	}
	for _, queue := range queues {
		if queue.Name == name {
			return queue.Limit, true
		}
	}
	return 0, false
}

func serialQueueScope(projectID, queueName string) string {
	projectID = strings.TrimSpace(projectID)
	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		return projectID
	}
	return projectID + sessionQueueScopeSeparator + queueName
}

func splitSerialQueueScope(scope string) (string, string) {
	projectID, queueName, found := strings.Cut(scope, sessionQueueScopeSeparator)
	if !found {
		return scope, ""
	}
	return projectID, queueName
}
