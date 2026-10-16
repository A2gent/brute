package storage

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const (
	MaxProviderFailures         = 20
	MaxProviderFailureTextBytes = 2048
)

// BoundProviderFailures handles both freshly built and JSON-decoded metadata.
// Keep diagnostics useful without rewriting megabytes of upstream error bodies
// on every save. Return a copy so persistence never mutates the caller's state.
func BoundProviderFailures(value interface{}) ([]interface{}, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var entries []interface{}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	if len(entries) > MaxProviderFailures {
		entries = append([]interface{}(nil), entries[len(entries)-MaxProviderFailures:]...)
	}
	for _, entry := range entries {
		fields, _ := entry.(map[string]interface{})
		for key, value := range fields {
			text, ok := value.(string)
			if !ok || len(text) <= MaxProviderFailureTextBytes {
				continue
			}
			end := MaxProviderFailureTextBytes - len("...")
			for !utf8.RuneStart(text[end]) {
				end--
			}
			fields[key] = text[:end] + "..."
		}
	}
	return entries, nil
}

// Keep the legacy column for old extraction migrations, but never populate it
// again. A transactional marker prevents large rewrites on subsequent startups.
func (s *SQLiteStore) migrateCompactSessionMetadata() error {
	exists, err := s.tableExists("session_metadata_compacted")
	if err != nil || exists {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE session_metadata_compacted (id INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id, json_extract(metadata, '$.provider_failures') FROM sessions
		WHERE CASE WHEN json_valid(metadata) THEN json_type(metadata, '$.provider_failures') = 'array' ELSE 0 END`)
	if err != nil {
		return err
	}
	type update struct{ id, payload string }
	var updates []update
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		bounded, err := BoundProviderFailures(json.RawMessage(raw))
		if err != nil {
			rows.Close()
			return err
		}
		payload, err := json.Marshal(bounded)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, update{id, string(payload)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := tx.Exec(`UPDATE sessions SET metadata = json_set(metadata, '$.provider_failures', json(?)) WHERE id = ?`, item.payload, item.id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET display_metadata = NULL WHERE display_metadata IS NOT NULL`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.checkpointMigrationWAL()
}

func (s *SQLiteStore) checkpointMigrationWAL() error {
	// Run outside the migration transaction, before opening our readers. The
	// journal size limit bounds retained WAL, not a live transaction or a reader
	// pinning old frames. An external reader may still prevent this checkpoint.
	var busy, log, checkpointed int
	if err := s.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint migration WAL: %w", err)
	}
	return nil
}
