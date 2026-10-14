package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Keep this predicate identical to the partial index so historical injected notes
// are found without scanning/decoding every message's metadata.
const injectedUserMessageSQL = `role = 'user' AND CASE WHEN json_valid(metadata) THEN lower(trim(CAST(json_extract(metadata, '$.injected_during_run') AS TEXT))) IN ('1', 'true') ELSE 0 END`

// GetSessionLiveState reads one snapshot of runtime fields and the unseen suffix
// plus injected notes. A missing (e.g. unsaved local) cursor returns only notes,
// never the full transcript. An empty cursor requests the entire suffix.
func (s *SQLiteStore) GetSessionLiveState(id, afterMessageID string) (*Session, error) {
	tx, err := s.sessionReader().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	live := &Session{ID: id}
	var metadata, progress sql.NullString
	err = tx.QueryRow(`SELECT status, task_progress, metadata, updated_at FROM sessions WHERE id = ?`, id).
		Scan(&live.Status, &progress, &metadata, &live.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	if err != nil {
		return nil, err
	}
	live.TaskProgress = progress.String
	if metadata.Valid && metadata.String != "" {
		if err := json.Unmarshal([]byte(metadata.String), &live.Metadata); err != nil {
			return nil, err
		}
	}
	// UNION deduplicates a new note also returned by the suffix range. Both
	// branches use indexes; the payloads are fetched only for selected rowids.
	query := `SELECT id, role, content, tool_calls, tool_results, metadata, timestamp FROM messages WHERE rowid IN (
		SELECT rowid FROM messages WHERE session_id = ? AND ` + injectedUserMessageSQL + `
		UNION SELECT rowid FROM messages WHERE session_id = ?`
	args := []interface{}{id, id}
	if afterMessageID != "" {
		query += ` AND (timestamp, rowid) > (SELECT timestamp, rowid FROM messages WHERE session_id = ? AND id = ?)`
		args = append(args, id, afterMessageID)
	}
	query += `) ORDER BY timestamp, rowid`
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var msg Message
		var calls, results, meta sql.NullString
		if err := rows.Scan(&msg.ID, &msg.Role, &msg.Content, &calls, &results, &meta, &msg.Timestamp); err != nil {
			return nil, err
		}
		if calls.Valid {
			msg.ToolCalls = json.RawMessage(calls.String)
		}
		if results.Valid {
			msg.ToolResults = json.RawMessage(results.String)
		}
		if meta.Valid && meta.String != "" {
			if err := json.Unmarshal([]byte(meta.String), &msg.Metadata); err != nil {
				return nil, err
			}
		}
		live.Messages = append(live.Messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return live, nil
}

// UpdateMessageMetadataKey never rewrites session state or transcript membership.
// Progress persistence must not race a user note, approval audit, or parking tool
// by saving an older in-memory copy of the entire session.
func (s *SQLiteStore) UpdateMessageMetadataKey(sessionID, messageID, key string, value interface{}, updatedAt time.Time) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE messages SET metadata = json_set(CASE WHEN json_valid(metadata) AND json_type(metadata) = 'object' THEN metadata ELSE '{}' END, ?, json(?)) WHERE session_id = ? AND id = ?`, `$.`+key, string(raw), sessionID, messageID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("message not found: %s", messageID)
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at = max(updated_at, ?) WHERE id = ?`, updatedAt, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}
