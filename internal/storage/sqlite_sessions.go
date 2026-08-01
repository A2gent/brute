package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// SaveSession saves a session to the database
func (s *SQLiteStore) SaveSession(sess *Session) error {
	save := func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		metadata, _ := json.Marshal(sess.Metadata)

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
					task_progress = excluded.task_progress,
					updated_at = excluded.updated_at
			`, sess.ID, sess.AgentID, sess.ParentID, sess.JobID, sess.ProjectID, sess.Title, sess.Summary, sess.Status, metadata, sess.TaskProgress, sess.CreatedAt, sess.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to save session: %w", err)
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

		currentMessageIDs := make(map[string]struct{}, len(sess.Messages))
		for _, msg := range sess.Messages {
			if msg.ID != "" {
				currentMessageIDs[msg.ID] = struct{}{}
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
		refreshFrom := len(sess.Messages) - 4
		if refreshFrom < 0 {
			refreshFrom = 0
		}
		for i, msg := range sess.Messages {
			if _, ok := existingMessageIDs[msg.ID]; ok && i < refreshFrom {
				continue
			}
			messageMetadata, _ := json.Marshal(msg.Metadata)
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
			`, msg.ID, sess.ID, msg.Role, msg.Content, msg.ToolCalls, msg.ToolResults, messageMetadata, msg.Timestamp)
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

// GetSession retrieves a session by ID
func (s *SQLiteStore) GetSession(id string) (*Session, error) {
	var sess Session
	var metadata sql.NullString
	var parentID sql.NullString
	var jobID sql.NullString
	var projectID sql.NullString
	var title sql.NullString
	var summary sql.NullString
	var taskProgress sql.NullString

	err := s.db.QueryRow(`
		SELECT id, agent_id, parent_id, job_id, project_id, title, summary, status, metadata, task_progress, created_at, updated_at
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

	// Load messages
	rows, err := s.db.Query(`
		SELECT id, role, content, tool_calls, tool_results, metadata, timestamp
		FROM messages WHERE session_id = ? ORDER BY timestamp
	`, id)
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

	err := s.db.QueryRow(`
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
	rows, err := s.db.Query(`
		SELECT id, agent_id, parent_id, job_id, project_id, title, summary, status, metadata, task_progress, created_at, updated_at
		FROM sessions
		ORDER BY created_at DESC
	`)
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
	rows, err := s.db.Query(`
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
	rows, err := s.db.Query(`
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
