package storage

import (
	"encoding/json"
	"fmt"
)

// CompressionEntry is stored independently from session metadata and transcripts.
type CompressionEntry struct {
	SessionID  string `json:"-"`
	Hash       string `json:"hash"`
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id"`
	Original   string `json:"original"`
	Compressed string `json:"compressed"`
}

type CompressionStore interface {
	SaveCompressionEntries(sessionID string, entries []CompressionEntry) error
	GetCompressionEntry(sessionID, hash string) (CompressionEntry, error)
}

func (s *SQLiteStore) SaveCompressionEntries(sessionID string, entries []CompressionEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, entry := range entries {
		payload, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		// Identical originals recur on request rebuilds; do not dirty their WAL pages.
		if _, err := tx.Exec(`INSERT INTO session_compression_entries (session_id, hash, payload) VALUES (?, ?, ?)
            ON CONFLICT(session_id, hash) DO UPDATE SET payload = excluded.payload
            WHERE session_compression_entries.payload <> excluded.payload`, sessionID, entry.Hash, string(payload)); err != nil {
			return fmt.Errorf("save compression entry: %w", err)
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) GetCompressionEntry(sessionID, hash string) (CompressionEntry, error) {
	var payload string
	if err := s.sessionReader().QueryRow(`SELECT payload FROM session_compression_entries WHERE session_id = ? AND hash = ?`, sessionID, hash).Scan(&payload); err != nil {
		return CompressionEntry{}, err
	}
	var entry CompressionEntry
	if err := json.Unmarshal([]byte(payload), &entry); err != nil {
		return CompressionEntry{}, err
	}
	entry.SessionID, entry.Hash = sessionID, hash
	return entry, nil
}

// Table creation is the migration marker. Extraction and stripping commit
// together, so a restart can neither lose originals nor repeat the large rewrite.
func (s *SQLiteStore) migrateSessionCompressionEntries() error {
	exists, err := s.tableExists("session_compression_entries")
	if err != nil || exists {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE session_compression_entries (
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		hash TEXT NOT NULL,
		payload TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, hash)
	)`); err != nil {
		return err
	}
	// SQL extraction avoids decoding all originals into Go during startup. Invalid
	// legacy metadata is left untouched; it must not abort startup or be destroyed.
	if _, err := tx.Exec(`INSERT INTO session_compression_entries (session_id, hash, payload, created_at)
		SELECT sessions.id, entry.key, entry.value, sessions.created_at
		FROM sessions, json_each(CASE WHEN json_valid(metadata) THEN
		 CASE WHEN json_type(metadata, '$.context_compression_store') = 'object'
		  THEN json_extract(metadata, '$.context_compression_store') ELSE '{}' END
		 ELSE '{}' END) AS entry
		WHERE entry.type = 'object'`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE sessions SET
		metadata = json_remove(metadata, '$.context_compression_store'),
		display_metadata = CASE WHEN json_valid(display_metadata) THEN json_remove(display_metadata, '$.context_compression_store') ELSE display_metadata END
		WHERE CASE WHEN json_valid(metadata) THEN json_type(metadata, '$.context_compression_store') IS NOT NULL ELSE 0 END`); err != nil {
		return err
	}
	return tx.Commit()
}
