package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ImageMigrationResult struct {
	BackupPath      string
	Messages        int
	PendingMessages int
	BeforeBytes     int64
	AfterBytes      int64
}

// MigrateSessionImages must run with Brute stopped. VACUUM INTO includes committed
// WAL content, unlike copying aagent.db. No rows are rewritten until backup succeeds.
// Extraction is transactional and repeatable; immutable files survive a rollback.
func MigrateSessionImages(dataPath, backupPath string) (result ImageMigrationResult, err error) {
	dataPath, err = filepath.Abs(dataPath)
	if err != nil {
		return result, err
	}
	dbPath := filepath.Join(dataPath, "aagent.db")
	info, err := os.Stat(dbPath)
	if err != nil {
		return result, err
	}
	result.BeforeBytes = info.Size()
	backupPath, err = filepath.Abs(backupPath)
	if err != nil {
		return result, err
	}
	result.BackupPath = backupPath
	// Reserve a private backup without overwriting an existing recovery point.
	f, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, fmt.Errorf("reserve backup: %w", err)
	}
	if err := f.Close(); err != nil {
		return result, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return result, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return result, err
	}
	if _, err = db.Exec("VACUUM INTO '" + strings.ReplaceAll(backupPath, "'", "''") + "'"); err != nil {
		return result, fmt.Errorf("backup failed (database unchanged): %w", err)
	}
	backup, err := os.OpenFile(backupPath, os.O_RDWR, 0)
	if err != nil {
		return result, err
	}
	err = backup.Sync()
	closeErr := backup.Close()
	if err != nil {
		return result, err
	}
	if closeErr != nil {
		return result, closeErr
	}
	parent, err := os.Open(filepath.Dir(backupPath))
	if err != nil {
		return result, err
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return result, err
	}
	tx, err := db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	cursor := ""
	for {
		var id, sessionID string
		var payload []byte
		// One row at a time keeps large legacy screenshots out of the migration heap.
		err = tx.QueryRow(`SELECT id, session_id, metadata FROM messages
   WHERE id > ? AND json_valid(metadata) AND json_type(metadata, '$.images') = 'array'
   ORDER BY id LIMIT 1`, cursor).Scan(&id, &sessionID, &payload)
		if err == sql.ErrNoRows {
			break
		}
		if err != nil {
			return result, err
		}
		cursor = id
		var metadata map[string]interface{}
		if err := json.Unmarshal(payload, &metadata); err != nil {
			return result, err
		}
		next, changed, err := externalizeMessageImages(dataPath, sessionID, metadata)
		if err != nil {
			return result, fmt.Errorf("message %s: %w", id, err)
		}
		if !changed {
			continue
		}
		encoded, err := json.Marshal(next)
		if err != nil {
			return result, err
		}
		if _, err := tx.Exec("UPDATE messages SET metadata = ? WHERE id = ?", encoded, id); err != nil {
			return result, err
		}
		result.Messages++
	}
	var pendingTable int
	if err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session_pending_messages'").Scan(&pendingTable); err != nil {
		return result, err
	}
	if pendingTable != 0 {
		cursor = ""
		for {
			var id, sessionID string
			var payload []byte
			err := tx.QueryRow(`SELECT id, session_id, images FROM session_pending_messages
                WHERE id > ? AND json_valid(images) AND json_type(images) = 'array' ORDER BY id LIMIT 1`, cursor).Scan(&id, &sessionID, &payload)
			if err == sql.ErrNoRows {
				break
			}
			if err != nil {
				return result, err
			}
			cursor = id
			var images interface{}
			if err := json.Unmarshal(payload, &images); err != nil {
				return result, err
			}
			next, changed, err := externalizeMessageImages(dataPath, sessionID, map[string]interface{}{"images": images})
			if err != nil {
				return result, fmt.Errorf("pending message %s: %w", id, err)
			}
			if !changed {
				continue
			}
			encoded, err := json.Marshal(next["images"])
			if err != nil {
				return result, err
			}
			if _, err := tx.Exec("UPDATE session_pending_messages SET images=? WHERE id=?", encoded, id); err != nil {
				return result, err
			}
			result.PendingMessages++
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	// VACUUM is deliberately opt-in maintenance, never part of server startup.
	if _, err := db.Exec("VACUUM"); err != nil {
		return result, fmt.Errorf("images migrated; vacuum failed: %w", err)
	}
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return result, err
	}
	info, err = os.Stat(dbPath)
	if err == nil {
		result.AfterBytes = info.Size()
	}
	return result, err
}
