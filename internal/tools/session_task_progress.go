package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SessionTaskProgressTool manages temporary task progress for the current session
type SessionTaskProgressTool struct {
	store TaskProgressStore
}

// TaskProgressStore interface for session task progress storage
type TaskProgressStore interface {
	GetSessionTaskProgress(sessionID string) (string, error)
	SetSessionTaskProgress(sessionID string, progress string) error
}

// SessionTaskProgressParams defines parameters for session task progress
type SessionTaskProgressParams struct {
	Action  string `json:"action"`            // "set", "get", "append"
	Content string `json:"content,omitempty"` // task progress content for set/append
}

// NewSessionTaskProgressTool creates a new session task progress tool
func NewSessionTaskProgressTool(store TaskProgressStore) *SessionTaskProgressTool {
	return &SessionTaskProgressTool{store: store}
}

func (t *SessionTaskProgressTool) Name() string {
	return "session_task_progress"
}

func (t *SessionTaskProgressTool) Description() string {
	return `Manage temporary task progress for the current session.

Use this tool to keep track of subtasks, planning steps, and completion state during session execution.
The task progress is session-scoped and automatically cleaned up when the session ends.

**Format:** Use a compact text format to minimize tokens:
- Each line represents a task or subtask
- Indent with 2 spaces for nested tasks
- Prefix: [ ] for pending, [x] for completed
- Example:
  [x] Step 1: Analyze requirements
    [ ] Step 1.1: Read files
    [x] Step 1.2: Identify patterns
  [ ] Step 2: Implement solution
  [ ] Step 3: Write tests

**Actions:**
- "get": Retrieve current task progress
- "set": Replace entire task progress (overwrites existing)
- "append": Add new tasks to existing progress

**Best practices:**
- Update progress after completing each subtask
- Keep descriptions concise (one line per task)
- Use nested tasks for complex workflows
- Mark tasks as [x] when completed

This replaces the need for temporary TODO files and provides visual progress tracking in the UI.`
}

func (t *SessionTaskProgressTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"get", "set", "append"},
				"description": "Action to perform: get (retrieve), set (replace), or append (add to existing)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Task progress content (required for set/append actions). Use compact format with [ ] and [x] markers.",
			},
		},
		"required": []string{"action"},
	}
}

func (t *SessionTaskProgressTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p SessionTaskProgressParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	// Extract session ID from context
	sessionID, ok := ctx.Value("session_id").(string)
	if !ok || sessionID == "" {
		return &Result{
			Success: false,
			Error:   "session_id not found in context",
		}, nil
	}

	switch p.Action {
	case "get":
		return t.handleGet(sessionID)
	case "set":
		return t.handleSet(sessionID, p.Content)
	case "append":
		return t.handleAppend(sessionID, p.Content)
	default:
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s (use: get, set, append)", p.Action),
		}, nil
	}
}

func (t *SessionTaskProgressTool) handleGet(sessionID string) (*Result, error) {
	progress, err := t.store.GetSessionTaskProgress(sessionID)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to get task progress: %v", err),
		}, nil
	}

	if progress == "" {
		return &Result{
			Success: true,
			Output:  "No task progress set for this session.",
		}, nil
	}

	return &Result{
		Success: true,
		Output:  progress,
	}, nil
}

func (t *SessionTaskProgressTool) handleSet(sessionID string, content string) (*Result, error) {
	if content == "" {
		return &Result{
			Success: false,
			Error:   "content is required for set action",
		}, nil
	}

	if err := t.store.SetSessionTaskProgress(sessionID, content); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to set task progress: %v", err),
		}, nil
	}

	stats := parseTaskStats(content)

	return &Result{
		Success: true,
		Output:  fmt.Sprintf("Task progress updated (%d/%d tasks completed)", stats.Completed, stats.Total),
		Metadata: map[string]interface{}{
			"total_tasks":     stats.Total,
			"completed_tasks": stats.Completed,
			"progress_pct":    stats.ProgressPct,
		},
	}, nil
}

func (t *SessionTaskProgressTool) handleAppend(sessionID string, content string) (*Result, error) {
	if content == "" {
		return &Result{
			Success: false,
			Error:   "content is required for append action",
		}, nil
	}

	existing, err := t.store.GetSessionTaskProgress(sessionID)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to get existing progress: %v", err),
		}, nil
	}

	var combined string
	if existing == "" {
		combined = content
	} else {
		combined = existing + "\n" + content
	}

	if err := t.store.SetSessionTaskProgress(sessionID, combined); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("failed to append task progress: %v", err),
		}, nil
	}

	stats := parseTaskStats(combined)

	return &Result{
		Success: true,
		Output:  fmt.Sprintf("Task progress appended (%d/%d tasks completed)", stats.Completed, stats.Total),
		Metadata: map[string]interface{}{
			"total_tasks":     stats.Total,
			"completed_tasks": stats.Completed,
			"progress_pct":    stats.ProgressPct,
		},
	}, nil
}

// TaskStats represents task progress statistics
type TaskStats struct {
	Total       int
	Completed   int
	ProgressPct int
}

// parseTaskStats extracts statistics from task progress text
func parseTaskStats(content string) TaskStats {
	lines := strings.Split(content, "\n")
	total := 0
	completed := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[ ]") {
			total++
		} else if strings.HasPrefix(trimmed, "[x]") || strings.HasPrefix(trimmed, "[X]") {
			total++
			completed++
		}
	}

	pct := 0
	if total > 0 {
		pct = (completed * 100) / total
	}

	return TaskStats{
		Total:       total,
		Completed:   completed,
		ProgressPct: pct,
	}
}
