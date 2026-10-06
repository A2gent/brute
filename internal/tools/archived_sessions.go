package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

const defaultArchivedSessionsLimit = 20
const maxArchivedSessionsLimit = 50
const defaultArchivedSessionMessages = 80

type ArchivedSessionsStore interface {
	ListSessions() ([]*storage.Session, error)
	GetSession(id string) (*storage.Session, error)
	SaveSession(sess *storage.Session) error
	GetProject(id string) (*storage.Project, error)
}

// ArchivedSessionsTool feeds the built-in archived-sessions review loop. Unlike
// project_session_history it is intentionally cross-project: the review job runs
// globally and routes findings (tasks, playbook lessons) back to each project.
type ArchivedSessionsTool struct {
	store ArchivedSessionsStore
}

type archivedSessionsParams struct {
	Action          string   `json:"action"`
	SessionID       string   `json:"session_id,omitempty"`
	SessionIDs      []string `json:"session_ids,omitempty"`
	Limit           int      `json:"limit,omitempty"`
	IncludeAnalyzed bool     `json:"include_analyzed,omitempty"`
	MaxMessages     int      `json:"max_messages,omitempty"`
}

func NewArchivedSessionsTool(store ArchivedSessionsStore) *ArchivedSessionsTool {
	return &ArchivedSessionsTool{store: store}
}

func (t *ArchivedSessionsTool) Name() string {
	return "archived_sessions"
}

func (t *ArchivedSessionsTool) Description() string {
	return "Review archived (completed) sessions across all projects. " +
		"action=list returns archived sessions not yet analyzed (with project name and folder), " +
		"action=get returns one transcript, action=mark_analyzed records that session_ids were reviewed so they are skipped next time."
}

func (t *ArchivedSessionsTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{"list", "get", "mark_analyzed"},
			},
			"session_id": map[string]interface{}{
				"type":        "string",
				"description": "Archived session ID for action=get.",
			},
			"session_ids": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Archived session IDs for action=mark_analyzed.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum sessions for action=list (default 20, max 50). Oldest archived first.",
			},
			"include_analyzed": map[string]interface{}{
				"type":        "boolean",
				"description": "Include already analyzed sessions in action=list (default false).",
			},
			"max_messages": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum transcript messages for action=get (default 80, max 200).",
			},
		},
		"required": []string{"action"},
	}
}

func (t *ArchivedSessionsTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	if t == nil || t.store == nil {
		return &Result{Success: false, Error: "session store is not configured"}, nil
	}
	var p archivedSessionsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	switch strings.TrimSpace(p.Action) {
	case "list":
		return t.list(p)
	case "get":
		return t.get(p)
	case "mark_analyzed":
		return t.markAnalyzed(p)
	default:
		return &Result{Success: false, Error: "unknown action: use list, get or mark_analyzed"}, nil
	}
}

func (t *ArchivedSessionsTool) list(p archivedSessionsParams) (*Result, error) {
	limit := clampInt(p.Limit, defaultArchivedSessionsLimit, 1, maxArchivedSessionsLimit)
	sessions, err := t.store.ListSessions()
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to list sessions: %v", err)}, nil
	}

	type item struct {
		sess       *storage.Session
		archivedAt time.Time
	}
	pending := make([]item, 0)
	for _, sess := range sessions {
		archivedAt := storage.SessionMetadataTime(sess.Metadata, storage.SessionArchivedAtKey)
		if archivedAt == nil {
			continue
		}
		if !p.IncludeAnalyzed && storage.SessionMetadataTime(sess.Metadata, storage.SessionArchiveAnalyzedAtKey) != nil {
			continue
		}
		pending = append(pending, item{sess: sess, archivedAt: *archivedAt})
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].archivedAt.Before(pending[j].archivedAt) })
	total := len(pending)
	if len(pending) > limit {
		pending = pending[:limit]
	}
	if total == 0 {
		return &Result{Success: true, Output: "No archived sessions pending analysis."}, nil
	}

	projects := map[string]string{}
	var b strings.Builder
	fmt.Fprintf(&b, "Archived sessions pending analysis (%d shown of %d):\n", len(pending), total)
	for _, it := range pending {
		projectID := sessionProjectID(it.sess)
		fmt.Fprintf(&b, "- %s | %s | %s | archived %s | project: %s",
			it.sess.ID, nonEmpty(it.sess.Title, "Untitled session"), it.sess.Status, formatSessionTime(it.archivedAt), t.describeProject(projects, projectID))
		if strings.TrimSpace(it.sess.Summary) != "" {
			fmt.Fprintf(&b, " | %s", singleLineTruncate(it.sess.Summary, 220))
		}
		fmt.Fprintln(&b)
	}
	return &Result{
		Success:  true,
		Output:   strings.TrimRight(b.String(), "\n"),
		Metadata: map[string]interface{}{"count": len(pending), "total": total},
	}, nil
}

// describeProject renders "name (id=..., folder=...)" and caches lookups per call.
func (t *ArchivedSessionsTool) describeProject(cache map[string]string, projectID string) string {
	if projectID == "" {
		return "none"
	}
	if cached, ok := cache[projectID]; ok {
		return cached
	}
	desc := "id=" + projectID
	if project, err := t.store.GetProject(projectID); err == nil && project != nil {
		folder := "none"
		if project.Folder != nil && strings.TrimSpace(*project.Folder) != "" {
			folder = strings.TrimSpace(*project.Folder)
		}
		desc = fmt.Sprintf("%s (id=%s, folder=%s)", project.Name, projectID, folder)
	}
	cache[projectID] = desc
	return desc
}

func (t *ArchivedSessionsTool) get(p archivedSessionsParams) (*Result, error) {
	id := strings.TrimSpace(p.SessionID)
	if id == "" {
		return &Result{Success: false, Error: "session_id is required for get action"}, nil
	}
	sess, err := t.store.GetSession(id)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to load session: %v", err)}, nil
	}
	if storage.SessionMetadataTime(sess.Metadata, storage.SessionArchivedAtKey) == nil {
		return &Result{Success: false, Error: "session is not archived"}, nil
	}
	maxMessages := clampInt(p.MaxMessages, defaultArchivedSessionMessages, 1, maxProjectSessionHistoryMessages)
	output, returned, omitted := formatSessionTranscript(sess, sessionProjectID(sess), maxMessages)
	return &Result{
		Success: true,
		Output:  output,
		Metadata: map[string]interface{}{
			"session_id":        sess.ID,
			"messages_returned": returned,
			"messages_omitted":  omitted,
		},
	}, nil
}

func (t *ArchivedSessionsTool) markAnalyzed(p archivedSessionsParams) (*Result, error) {
	if len(p.SessionIDs) == 0 && strings.TrimSpace(p.SessionID) != "" {
		p.SessionIDs = []string{p.SessionID}
	}
	if len(p.SessionIDs) == 0 {
		return &Result{Success: false, Error: "session_ids is required for mark_analyzed action"}, nil
	}
	now := time.Now()
	marked := make([]string, 0, len(p.SessionIDs))
	var failures []string
	for _, raw := range p.SessionIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		sess, err := t.store.GetSession(id)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		if storage.SessionMetadataTime(sess.Metadata, storage.SessionArchivedAtKey) == nil {
			failures = append(failures, id+": not archived")
			continue
		}
		sess.Metadata = storage.SetSessionMetadataTime(sess.Metadata, storage.SessionArchiveAnalyzedAtKey, &now)
		if err := t.store.SaveSession(sess); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		marked = append(marked, id)
	}
	output := fmt.Sprintf("Marked %d session(s) as analyzed.", len(marked))
	if len(failures) > 0 {
		output += " Failed: " + strings.Join(failures, "; ")
	}
	return &Result{
		Success:  len(failures) == 0,
		Output:   output,
		Error:    strings.Join(failures, "; "),
		Metadata: map[string]interface{}{"marked": marked},
	}, nil
}

var _ Tool = (*ArchivedSessionsTool)(nil)
