package storage

import (
	"encoding/json"
	"fmt"
)

const SystemPromptSnapshotMetadataKey = "system_prompt_snapshot"

// SystemPromptSnapshotStore keeps frozen prompts out of ordinary session reads.
// Missing snapshots return sql.ErrNoRows; SaveSession never changes this state.
type SystemPromptSnapshotStore interface {
	GetSystemPromptSnapshot(sessionID string) (json.RawMessage, error)
	SaveSystemPromptSnapshot(sessionID string, payload json.RawMessage) error
}

func (s *SQLiteStore) GetSystemPromptSnapshot(sessionID string) (json.RawMessage, error) {
	var payload string
	if err := s.sessionReader().QueryRow(`SELECT payload FROM session_system_prompt_snapshots WHERE session_id = ?`, sessionID).Scan(&payload); err != nil {
		return nil, err
	}
	return json.RawMessage(payload), nil
}

func (s *SQLiteStore) SaveSystemPromptSnapshot(sessionID string, payload json.RawMessage) error {
	if !json.Valid(payload) {
		return fmt.Errorf("invalid system prompt snapshot JSON")
	}
	save := func() error {
		// Reusing a frozen prefix must not dirty its (potentially large) WAL pages.
		_, err := s.db.Exec(`INSERT INTO session_system_prompt_snapshots (session_id, payload) VALUES (?, ?)
			ON CONFLICT(session_id) DO UPDATE SET payload = excluded.payload
			WHERE session_system_prompt_snapshots.payload <> excluded.payload`, sessionID, string(payload))
		return err
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

// Table creation is the migration marker. Copy and strip commit together, so a
// failed migration is retryable and subsequent startups never rewrite snapshots.
func (s *SQLiteStore) migrateSessionSystemPromptSnapshots() error {
	exists, err := s.tableExists("session_system_prompt_snapshots")
	if err != nil || exists {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE session_system_prompt_snapshots (
		session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
		payload TEXT NOT NULL
	)`); err != nil {
		return err
	}
	// Full metadata is authoritative. Recover display-only snapshots too, without
	// decoding/recomposing prompts or changing the bytes of their cached prefixes.
	if _, err := tx.Exec(`INSERT INTO session_system_prompt_snapshots (session_id, payload)
		SELECT id, CASE WHEN json_type(CASE WHEN json_valid(metadata) THEN metadata ELSE '{}' END, '$.system_prompt_snapshot') = 'object'
			THEN metadata -> '$.system_prompt_snapshot' ELSE display_metadata -> '$.system_prompt_snapshot' END
		FROM sessions
		WHERE CASE WHEN json_valid(metadata) THEN json_type(metadata, '$.system_prompt_snapshot') = 'object' ELSE 0 END
			OR CASE WHEN json_valid(display_metadata) THEN json_type(display_metadata, '$.system_prompt_snapshot') = 'object' ELSE 0 END`); err != nil {
		return err
	}
	// Leave malformed legacy JSON untouched rather than destroying unrelated state.
	if _, err := tx.Exec(`UPDATE sessions SET
		metadata = CASE WHEN json_valid(metadata) THEN json_remove(metadata, '$.system_prompt_snapshot') ELSE metadata END,
		display_metadata = CASE WHEN json_valid(display_metadata) THEN json_remove(display_metadata, '$.system_prompt_snapshot') ELSE display_metadata END
		WHERE id IN (SELECT session_id FROM session_system_prompt_snapshots)`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.checkpointMigrationWAL()
}
