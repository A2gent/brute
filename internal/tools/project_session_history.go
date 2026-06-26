package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/storage"
)

const defaultProjectSessionHistoryLimit = 10
const maxProjectSessionHistoryLimit = 50
const defaultProjectSessionHistoryMessages = 40
const maxProjectSessionHistoryMessages = 200

type ProjectSessionHistoryStore interface {
	GetSession(id string) (*storage.Session, error)
	GetSessionSummary(id string) (*storage.Session, error)
	ListSessions() ([]*storage.Session, error)
}

// ProjectSessionHistoryTool exposes past sessions from the same project as the
// running agent. The tool intentionally derives project scope from session_id in
// context instead of accepting arbitrary project IDs, so agents cannot browse
// unrelated project transcripts by guessing identifiers.
type ProjectSessionHistoryTool struct {
	store ProjectSessionHistoryStore
}

type ProjectSessionHistoryParams struct {
	Action         string `json:"action"`
	SessionID      string `json:"session_id,omitempty"`
	Query          string `json:"query,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	IncludeCurrent bool   `json:"include_current,omitempty"`
	MaxMessages    int    `json:"max_messages,omitempty"`
}

func NewProjectSessionHistoryTool(store ProjectSessionHistoryStore) *ProjectSessionHistoryTool {
	return &ProjectSessionHistoryTool{store: store}
}

func (t *ProjectSessionHistoryTool) Name() string {
	return "project_session_history"
}

func (t *ProjectSessionHistoryTool) Description() string {
	return "List or read past sessions that belong to the current session's project. " +
		"Use action=list to discover relevant project history, then action=get with a returned session_id to read a transcript. " +
		"The tool is scoped automatically to the current project and excludes the current session by default."
}

func (t *ProjectSessionHistoryTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"list", "get"},
				"description": "list returns compact summaries for past sessions in the current project; get returns one scoped transcript.",
			},
			"session_id": map[string]interface{}{
				"type":        "string",
				"description": "Session ID to read when action=get. The session must belong to the current project.",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Optional case-insensitive filter for action=list, matched against title, summary, status, and session ID.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum sessions to return for action=list (default 10, max 50).",
			},
			"include_current": map[string]interface{}{
				"type":        "boolean",
				"description": "Include the current running session in action=list results (default false).",
			},
			"max_messages": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum transcript messages for action=get (default 40, max 200).",
			},
		},
		"required": []string{"action"},
	}
}

func (t *ProjectSessionHistoryTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	if t == nil || t.store == nil {
		return &Result{Success: false, Error: "session history store is not configured"}, nil
	}

	var p ProjectSessionHistoryParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	currentSessionID, _ := ctx.Value("session_id").(string)
	currentSessionID = strings.TrimSpace(currentSessionID)
	if currentSessionID == "" {
		return &Result{Success: false, Error: "session_id not found in context"}, nil
	}

	current, err := t.store.GetSessionSummary(currentSessionID)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to load current session: %v", err)}, nil
	}
	projectID := sessionProjectID(current)
	if projectID == "" {
		return &Result{Success: false, Error: "current session is not associated with a project"}, nil
	}

	switch strings.TrimSpace(p.Action) {
	case "list":
		return t.handleList(projectID, currentSessionID, p)
	case "get":
		return t.handleGet(projectID, p)
	default:
		return &Result{Success: false, Error: "unknown action: use list or get"}, nil
	}
}

func (t *ProjectSessionHistoryTool) handleList(projectID string, currentSessionID string, p ProjectSessionHistoryParams) (*Result, error) {
	limit := clampInt(p.Limit, defaultProjectSessionHistoryLimit, 1, maxProjectSessionHistoryLimit)
	query := strings.ToLower(strings.TrimSpace(p.Query))

	sessions, err := t.store.ListSessions()
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to list sessions: %v", err)}, nil
	}

	matched := make([]*storage.Session, 0, len(sessions))
	for _, sess := range sessions {
		if sess == nil || sessionProjectID(sess) != projectID {
			continue
		}
		if !p.IncludeCurrent && sess.ID == currentSessionID {
			continue
		}
		if query != "" && !sessionMatchesQuery(sess, query) {
			continue
		}
		matched = append(matched, sess)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].UpdatedAt.After(matched[j].UpdatedAt)
	})
	if len(matched) > limit {
		matched = matched[:limit]
	}

	if len(matched) == 0 {
		return &Result{Success: true, Output: "No past sessions found in the current project."}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Project session history (%d shown, project_id=%s):\n", len(matched), projectID)
	for _, sess := range matched {
		fmt.Fprintf(&b, "- %s | %s | %s | updated %s", sess.ID, nonEmpty(sess.Title, "Untitled session"), sess.Status, formatSessionTime(sess.UpdatedAt))
		if strings.TrimSpace(sess.Summary) != "" {
			fmt.Fprintf(&b, " | %s", singleLineTruncate(sess.Summary, 220))
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintln(&b, "Use action=get with one of these session_id values to read a scoped transcript.")

	return &Result{
		Success: true,
		Output:  strings.TrimRight(b.String(), "\n"),
		Metadata: map[string]interface{}{
			"project_id": projectID,
			"count":      len(matched),
		},
	}, nil
}

func (t *ProjectSessionHistoryTool) handleGet(projectID string, p ProjectSessionHistoryParams) (*Result, error) {
	targetID := strings.TrimSpace(p.SessionID)
	if targetID == "" {
		return &Result{Success: false, Error: "session_id is required for get action"}, nil
	}

	sess, err := t.store.GetSession(targetID)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to load session: %v", err)}, nil
	}
	if sessionProjectID(sess) != projectID {
		return &Result{Success: false, Error: "requested session is outside the current project scope"}, nil
	}

	maxMessages := clampInt(p.MaxMessages, defaultProjectSessionHistoryMessages, 1, maxProjectSessionHistoryMessages)
	messages := sess.Messages
	omitted := 0
	if len(messages) > maxMessages {
		omitted = len(messages) - maxMessages
		messages = messages[omitted:]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Session %s (project_id=%s)\n", sess.ID, projectID)
	fmt.Fprintf(&b, "Title: %s\nStatus: %s\nCreated: %s\nUpdated: %s\n", nonEmpty(sess.Title, "Untitled session"), sess.Status, formatSessionTime(sess.CreatedAt), formatSessionTime(sess.UpdatedAt))
	if strings.TrimSpace(sess.Summary) != "" {
		fmt.Fprintf(&b, "Summary: %s\n", strings.TrimSpace(sess.Summary))
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\nTranscript (last %d messages, %d earlier omitted):\n", len(messages), omitted)
	} else {
		fmt.Fprintf(&b, "\nTranscript (%d messages):\n", len(messages))
	}
	for _, msg := range messages {
		role := nonEmpty(msg.Role, "message")
		content := strings.TrimSpace(msg.Content)
		if content == "" && len(msg.ToolCalls) > 0 {
			content = "[tool calls]"
		}
		if content == "" && len(msg.ToolResults) > 0 {
			content = "[tool results]"
		}
		fmt.Fprintf(&b, "\n[%s at %s]\n%s\n", role, formatSessionTime(msg.Timestamp), truncateRunes(content, 4000))
		if len(msg.ToolCalls) > 0 {
			fmt.Fprintf(&b, "Tool calls: %s\n", truncateRunes(string(msg.ToolCalls), 3000))
		}
		if len(msg.ToolResults) > 0 {
			fmt.Fprintf(&b, "Tool results: %s\n", truncateRunes(string(msg.ToolResults), 3000))
		}
	}

	return &Result{
		Success: true,
		Output:  strings.TrimRight(b.String(), "\n"),
		Metadata: map[string]interface{}{
			"project_id":        projectID,
			"session_id":        sess.ID,
			"message_count":     len(sess.Messages),
			"messages_returned": len(messages),
			"messages_omitted":  omitted,
		},
	}, nil
}

func sessionProjectID(sess *storage.Session) string {
	if sess == nil || sess.ProjectID == nil {
		return ""
	}
	return strings.TrimSpace(*sess.ProjectID)
}

func sessionMatchesQuery(sess *storage.Session, query string) bool {
	if sess == nil || query == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{sess.ID, sess.Title, sess.Summary, sess.Status}, "\n"))
	return strings.Contains(haystack, query)
}

func clampInt(value int, defaultValue int, minValue int, maxValue int) int {
	if value == 0 {
		return defaultValue
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func formatSessionTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.RFC3339)
}

func nonEmpty(value string, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func singleLineTruncate(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	return truncateRunes(value, limit)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "…"
}

var _ Tool = (*ProjectSessionHistoryTool)(nil)
