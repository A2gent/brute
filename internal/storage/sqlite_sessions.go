package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrMessageCursorMissing = errors.New("message history cursor no longer exists")

// SaveSession saves a session to the database
func (s *SQLiteStore) SaveSession(sess *Session) error {
	ids := make([]string, len(sess.Messages))
	for i, msg := range sess.Messages {
		ids[i] = msg.ID
	}
	return s.saveSessionMessages(sess, ids, nil, func(i int) Message { return sess.Messages[i] }, false)
}

// SaveSessionIncremental materializes only new messages, the mutable tail, and
// explicitly changed historical messages. Membership still reconciles removals.
func (s *SQLiteStore) SaveSessionIncremental(sess *Session, ids []string, dirty map[string]bool, materialize func(int) Message) error {
	return s.saveSessionMessages(sess, ids, dirty, materialize, true)
}

func (s *SQLiteStore) saveSessionMessages(sess *Session, ids []string, dirty map[string]bool, materialize func(int) Message, incremental bool) error {
	save := func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		// Large immutable state has independent persistence; even in-memory display
		// copies must be filtered before marshalling, not just hidden in projections.
		sessionMetadata := make(map[string]interface{}, len(sess.Metadata))
		for key, value := range sess.Metadata {
			if key != "context_compression_store" && key != SystemPromptSnapshotMetadataKey {
				if key == "provider_failures" {
					var err error
					value, err = BoundProviderFailures(value)
					if err != nil {
						return fmt.Errorf("bound provider failures: %w", err)
					}
				}
				sessionMetadata[key] = value
			}
		}
		metadata, err := json.Marshal(sessionMetadata)
		if err != nil {
			return fmt.Errorf("marshal session metadata: %w", err)
		}

		// Upsert session
		_, err = tx.Exec(`
				INSERT INTO sessions (id, agent_id, parent_id, job_id, project_id, title, summary, status, metadata, task_progress, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET
					parent_id = excluded.parent_id,
					job_id = excluded.job_id,
					project_id = excluded.project_id,
					title = excluded.title,
					summary = excluded.summary,
					status = excluded.status,
					metadata = excluded.metadata,
					display_metadata = NULL,
					task_progress = excluded.task_progress,
					updated_at = excluded.updated_at
			`, sess.ID, sess.AgentID, sess.ParentID, sess.JobID, sess.ProjectID, sess.Title, sess.Summary, sess.Status, metadata, sess.TaskProgress, sess.CreatedAt, sess.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to save session: %w", err)
		}

		if sess.SystemPromptSnapshotSourceID != "" {
			// Copy any materialized prefix before the continuation becomes visible.
			// Sources that never composed a snapshot have no frozen prefix to copy.
			if _, err := tx.Exec(`INSERT INTO session_system_prompt_snapshots (session_id, payload)
				SELECT ?, payload FROM session_system_prompt_snapshots WHERE session_id = ?
				ON CONFLICT(session_id) DO NOTHING`, sess.ID, sess.SystemPromptSnapshotSourceID); err != nil {
				return fmt.Errorf("copy system prompt snapshot: %w", err)
			}
		}

		existingMessageIDs := make(map[string]struct{})
		rows, err := tx.Query("SELECT id FROM messages WHERE session_id = ?", sess.ID)
		if err != nil {
			return fmt.Errorf("failed to load existing messages: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("failed to scan existing message: %w", err)
			}
			existingMessageIDs[id] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("failed to close existing message rows: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("failed to iterate existing messages: %w", err)
		}

		currentMessageIDs := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if id != "" {
				currentMessageIDs[id] = struct{}{}
			}
		}
		for id := range existingMessageIDs {
			if _, ok := currentMessageIDs[id]; ok {
				continue
			}
			if _, err := tx.Exec("DELETE FROM messages WHERE id = ? AND session_id = ?", id, sess.ID); err != nil {
				return fmt.Errorf("failed to delete stale message: %w", err)
			}
		}

		// Insert new messages and refresh the tail, where in-flight agent runs
		// are most likely to add metadata or recover from interrupted tool calls.
		refreshFrom := len(ids) - 4
		if refreshFrom < 0 {
			refreshFrom = 0
		}
		for i, id := range ids {
			if _, ok := existingMessageIDs[id]; incremental && ok && i < refreshFrom && !dirty[id] {
				continue
			}
			msg := materialize(i)
			if msg.ID != id {
				return fmt.Errorf("message identity changed during save")
			}
			storedMetadata, _, err := externalizeMessageImages(s.dataPath, sess.ID, msg.Metadata)
			if err != nil {
				return err
			}
			messageMetadata, err := json.Marshal(storedMetadata)
			if err != nil {
				return err
			}
			// The driver stores time via t.String(), which appends the monotonic
			// clock ("m=+0.07") only for fresh time.Now() values. Rewriting a reloaded
			// message then yields a shorter string that sorts before an equal-instant
			// neighbour, reordering history; Round(0) strips the monotonic reading.
			_, err = tx.Exec(`
				INSERT INTO messages (id, session_id, role, content, tool_calls, tool_results, metadata, timestamp)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET
					session_id = excluded.session_id,
					role = excluded.role,
					content = excluded.content,
					tool_calls = excluded.tool_calls,
					tool_results = excluded.tool_results,
					metadata = excluded.metadata,
					timestamp = excluded.timestamp
			`, msg.ID, sess.ID, msg.Role, msg.Content, msg.ToolCalls, msg.ToolResults, messageMetadata, msg.Timestamp.Round(0))
			if err != nil {
				return fmt.Errorf("failed to save message: %w", err)
			}
		}

		return tx.Commit()
	}

	if err := save(); err != nil {
		if reopenErr := s.reopenOnReadonly(err); reopenErr != nil {
			return reopenErr
		}
		if isSQLiteReadonlyError(err) {
			return save()
		}
		return err
	}
	return nil
}

// UpdateSessionSummary writes only the summary column so a late async summary
// cannot clobber messages/status saved by a newer run of the same session.
func (s *SQLiteStore) UpdateSessionSummary(id, summary string) error {
	update := func() error {
		res, err := s.db.Exec(`UPDATE sessions SET summary = ? WHERE id = ?`, summary, id)
		if err != nil {
			return fmt.Errorf("failed to update session summary: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("session not found: %s", id)
		}
		return nil
	}
	if err := update(); err != nil {
		if reopenErr := s.reopenOnReadonly(err); reopenErr != nil {
			return reopenErr
		}
		if isSQLiteReadonlyError(err) {
			return update()
		}
		return err
	}
	return nil
}

// GetSession retrieves a session by ID
func (s *SQLiteStore) GetSession(id string) (*Session, error) {
	return s.getSession(id, "metadata", true, 0, "")
}

// GetSessionForDisplay excludes internal compression state before decoding metadata.
// Agent reads continue to use GetSession and retain the complete state.
func (s *SQLiteStore) GetSessionForDisplay(id string, includeMessages bool) (*Session, error) {
	return s.getSession(id, displaySessionMetadataSQL, includeMessages, 0, "")
}

// GetSessionPageForDisplay bounds reads for completed/failed sessions. Live
// sessions retain the full transcript required by streaming reconciliation.
func (s *SQLiteStore) GetSessionPageForDisplay(id string, limit int, before string) (*Session, bool, error) {
	if limit < 1 || limit > 500 {
		return nil, false, fmt.Errorf("message limit must be between 1 and 500")
	}
	row, err := s.getSession(id, displaySessionMetadataSQL, true, limit+1, before)
	if err != nil {
		return nil, false, err
	}
	hasMore := (row.Status == "completed" || row.Status == "failed") && len(row.Messages) > limit
	if hasMore {
		row.Messages = row.Messages[len(row.Messages)-limit:]
	}
	return row, hasMore, nil
}

// Metadata is authoritative; filter legacy imports without reading stale projections.
const displaySessionMetadataSQL = "CASE WHEN json_valid(metadata) THEN json_remove(metadata, '$.context_compression_store', '$.system_prompt_snapshot') ELSE metadata END"

func (s *SQLiteStore) getSession(id, metadataSQL string, includeMessages bool, limit int, before string) (*Session, error) {
	var sess Session
	var metadata sql.NullString
	var parentID sql.NullString
	var jobID sql.NullString
	var projectID sql.NullString
	var title sql.NullString
	var summary sql.NullString
	var taskProgress sql.NullString

	err := s.sessionReader().QueryRow(`
		SELECT id, agent_id, parent_id, job_id, project_id, title, summary, status, `+metadataSQL+`, task_progress, created_at, updated_at
		FROM sessions WHERE id = ?
	`, id).Scan(&sess.ID, &sess.AgentID, &parentID, &jobID, &projectID, &title, &summary, &sess.Status, &metadata, &taskProgress, &sess.CreatedAt, &sess.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	if parentID.Valid {
		sess.ParentID = &parentID.String
	}
	if jobID.Valid {
		sess.JobID = &jobID.String
	}
	if projectID.Valid {
		sess.ProjectID = &projectID.String
	}
	if title.Valid {
		sess.Title = title.String
	}
	if summary.Valid {
		sess.Summary = summary.String
	}
	if metadata.Valid {
		json.Unmarshal([]byte(metadata.String), &sess.Metadata)
	}
	if taskProgress.Valid {
		sess.TaskProgress = taskProgress.String
	}

	if !includeMessages {
		return &sess, nil
	}

	// Load newest messages first for a page, then reverse to chronological order.
	query := `SELECT id, role, content, tool_calls, tool_results, metadata, timestamp FROM messages WHERE session_id = ?`
	args := []interface{}{id}
	paged := limit > 0 && (sess.Status == "completed" || sess.Status == "failed")
	if paged && before != "" {
		var exists bool
		if err := s.sessionReader().QueryRow("SELECT EXISTS(SELECT 1 FROM messages WHERE session_id = ? AND id = ?)", id, before).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrMessageCursorMissing
		}
		query += " AND (timestamp, rowid) < (SELECT timestamp, rowid FROM messages WHERE session_id = ? AND id = ?)"
		args = append(args, id, before)
	}
	if paged {
		query += " ORDER BY timestamp DESC, rowid DESC LIMIT ?"
		args = append(args, limit)
	} else {
		query += " ORDER BY timestamp, rowid"
	}
	rows, err := s.sessionReader().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var msg Message
		var toolCalls, toolResults, metadata sql.NullString

		err := rows.Scan(&msg.ID, &msg.Role, &msg.Content, &toolCalls, &toolResults, &metadata, &msg.Timestamp)
		if err != nil {
			return nil, err
		}

		if toolCalls.Valid {
			msg.ToolCalls = json.RawMessage(toolCalls.String)
		}
		if toolResults.Valid {
			msg.ToolResults = json.RawMessage(toolResults.String)
		}
		if metadata.Valid && metadata.String != "" {
			json.Unmarshal([]byte(metadata.String), &msg.Metadata)
		}

		sess.Messages = append(sess.Messages, msg)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if paged {
		for i, j := 0, len(sess.Messages)-1; i < j; i, j = i+1, j-1 {
			sess.Messages[i], sess.Messages[j] = sess.Messages[j], sess.Messages[i]
		}
	}
	return &sess, nil
}

// GetSessionSummary retrieves a session by ID without loading messages or bulky metadata.
func (s *SQLiteStore) GetSessionSummary(id string) (*Session, error) {
	var sess Session
	var parentID sql.NullString
	var jobID sql.NullString
	var projectID sql.NullString
	var title sql.NullString
	var summary sql.NullString
	var taskProgress sql.NullString

	err := s.sessionReader().QueryRow(`
		SELECT id, agent_id, parent_id, job_id, project_id, title, summary, status, task_progress, created_at, updated_at
		FROM sessions WHERE id = ?
	`, id).Scan(&sess.ID, &sess.AgentID, &parentID, &jobID, &projectID, &title, &summary, &sess.Status, &taskProgress, &sess.CreatedAt, &sess.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	if parentID.Valid {
		sess.ParentID = &parentID.String
	}
	if jobID.Valid {
		sess.JobID = &jobID.String
	}
	if projectID.Valid {
		sess.ProjectID = &projectID.String
	}
	if title.Valid {
		sess.Title = title.String
	}
	if summary.Valid {
		sess.Summary = summary.String
	}
	if taskProgress.Valid {
		sess.TaskProgress = taskProgress.String
	}

	return &sess, nil
}

// ListSessions lists all sessions, including sessions created by recurring jobs.
func (s *SQLiteStore) ListSessions() ([]*Session, error) {
	return s.listSessions("metadata", "")
}

// ListSessionsForDisplay filters projects in SQL and avoids decoding runtime-only state.
func (s *SQLiteStore) ListSessionsForDisplay(projectID string) ([]*Session, error) {
	return s.listSessions(displaySessionMetadataSQL, projectID)
}

func (s *SQLiteStore) ListRelatedSessionsForDisplay(projectID, sessionID string) ([]*Session, error) {
	return s.listSessionRows(displaySessionMetadataSQL, projectID, sessionID)
}

func (s *SQLiteStore) listSessions(metadataSQL, projectID string) ([]*Session, error) {
	return s.listSessionRows(metadataSQL, projectID, "")
}

func (s *SQLiteStore) listSessionRows(metadataSQL, projectID, relatedTo string) ([]*Session, error) {
	where := ""
	var args []interface{}
	if projectID != "" {
		where = " WHERE project_id = ?"
		args = append(args, projectID)
	}
	if relatedTo != "" {
		if where == "" {
			where = " WHERE "
		} else {
			where += " AND "
		}
		where += "(parent_id = ? OR id = (SELECT parent_id FROM sessions WHERE id = ?))"
		args = append(args, relatedTo, relatedTo)
	}
	rows, err := s.sessionReader().Query(`
		SELECT id, agent_id, parent_id, job_id, project_id, title, summary, status, `+metadataSQL+`, task_progress, created_at, updated_at
		FROM sessions`+where+`
		ORDER BY created_at DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		var sess Session
		var parentID, jobID, projectID sql.NullString
		var title sql.NullString
		var summary sql.NullString
		var metadata sql.NullString
		var taskProgress sql.NullString

		err := rows.Scan(&sess.ID, &sess.AgentID, &parentID, &jobID, &projectID, &title, &summary, &sess.Status, &metadata, &taskProgress, &sess.CreatedAt, &sess.UpdatedAt)
		if err != nil {
			return nil, err
		}

		if parentID.Valid {
			sess.ParentID = &parentID.String
		}
		if jobID.Valid {
			sess.JobID = &jobID.String
		}
		if projectID.Valid {
			sess.ProjectID = &projectID.String
		}
		if title.Valid {
			sess.Title = title.String
		}
		if summary.Valid {
			sess.Summary = summary.String
		}
		if metadata.Valid && metadata.String != "" {
			_ = json.Unmarshal([]byte(metadata.String), &sess.Metadata)
		}
		if taskProgress.Valid {
			sess.TaskProgress = taskProgress.String
		}

		sessions = append(sessions, &sess)
	}

	return sessions, nil
}

// SearchSessionDialogues returns sessions whose user or assistant message content contains query.
// Tool payload columns are intentionally not selected so tool calls and file contents cannot match.
func (s *SQLiteStore) SearchSessionDialogues(query, projectID string) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	args := []interface{}{strings.ToLower(query)}
	projectFilter := ""
	if projectID != "" {
		projectFilter = " AND sessions.project_id = ?"
		args = append(args, projectID)
	}
	rows, err := s.sessionReader().Query(`
		SELECT DISTINCT messages.session_id
		FROM messages
		JOIN sessions ON sessions.id = messages.session_id
		WHERE messages.role IN ('user', 'assistant')
		  AND INSTR(LOWER(messages.content), ?) > 0`+projectFilter, args...)
	if err != nil {
		return nil, fmt.Errorf("search session dialogues: %w", err)
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, fmt.Errorf("scan session dialogue match: %w", err)
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session dialogue matches: %w", err)
	}
	return sessionIDs, nil
}

// ListSessionsByJob returns all sessions associated with a specific job
func (s *SQLiteStore) ListSessionsByJob(jobID string) ([]*Session, error) {
	rows, err := s.sessionReader().Query(`
		SELECT id, agent_id, parent_id, job_id, project_id, title, summary, status, metadata, task_progress, created_at, updated_at
		FROM sessions 
		WHERE job_id = ?
		ORDER BY created_at DESC
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		var sess Session
		var parentID, jobID, projectID sql.NullString
		var title sql.NullString
		var summary sql.NullString
		var metadata sql.NullString
		var taskProgress sql.NullString

		err := rows.Scan(&sess.ID, &sess.AgentID, &parentID, &jobID, &projectID, &title, &summary, &sess.Status, &metadata, &taskProgress, &sess.CreatedAt, &sess.UpdatedAt)
		if err != nil {
			return nil, err
		}

		if parentID.Valid {
			sess.ParentID = &parentID.String
		}
		if jobID.Valid {
			sess.JobID = &jobID.String
		}
		if projectID.Valid {
			sess.ProjectID = &projectID.String
		}
		if title.Valid {
			sess.Title = title.String
		}
		if summary.Valid {
			sess.Summary = summary.String
		}
		if metadata.Valid && metadata.String != "" {
			_ = json.Unmarshal([]byte(metadata.String), &sess.Metadata)
		}
		if taskProgress.Valid {
			sess.TaskProgress = taskProgress.String
		}

		sessions = append(sessions, &sess)
	}

	return sessions, nil
}

// DeleteSession deletes a session.
func (s *SQLiteStore) DeleteSession(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete all descendant sessions recursively as well.
	// Delete messages explicitly because SQLite foreign key cascades may be disabled.
	if _, err := tx.Exec(`
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM sessions WHERE id = ?
			UNION ALL
			SELECT s.id
			FROM sessions s
			INNER JOIN descendants d ON s.parent_id = d.id
		)
		DELETE FROM messages
		WHERE session_id IN (SELECT id FROM descendants)
	`, id); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM sessions WHERE id = ?
			UNION ALL
			SELECT s.id
			FROM sessions s
			INNER JOIN descendants d ON s.parent_id = d.id
		)
		DELETE FROM sessions
		WHERE id IN (SELECT id FROM descendants)
	`, id); err != nil {
		return err
	}

	return tx.Commit()
}

// SessionRef identifies a session row and its project.
type SessionRef struct {
	ID        string
	ProjectID string
}

// ListSessionTree returns the root and every descendant that DeleteSession
// would remove, so callers can announce the cascade after it commits.
func (s *SQLiteStore) ListSessionTree(rootID string) ([]SessionRef, error) {
	rows, err := s.sessionReader().Query(`
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM sessions WHERE id = ?
			UNION ALL
			SELECT s.id
			FROM sessions s
			INNER JOIN descendants d ON s.parent_id = d.id
		)
		SELECT id, COALESCE(project_id, '') FROM sessions WHERE id IN (SELECT id FROM descendants)
	`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []SessionRef
	for rows.Next() {
		var ref SessionRef
		if err := rows.Scan(&ref.ID, &ref.ProjectID); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}
