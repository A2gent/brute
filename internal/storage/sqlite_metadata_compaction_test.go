package storage

import (
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSessionMetadataStoredOnceAndProviderFailuresBounded(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := compressionTestSession()
	failures := make([]map[string]interface{}, 25)
	for i := range failures {
		failures[i] = map[string]interface{}{"attempt": i, "reason": strings.Repeat("error 🤖", 1000)}
	}
	row.Metadata = map[string]interface{}{"provider": "test", "provider_failures": failures}
	for i := 0; i < 2; i++ {
		if err := store.SaveSession(row); err != nil {
			t.Fatal(err)
		}
		var display sql.NullString
		if err := store.db.QueryRow(`SELECT display_metadata FROM sessions WHERE id = ?`, row.ID).Scan(&display); err != nil {
			t.Fatal(err)
		}
		if display.Valid {
			t.Fatal("duplicated metadata persisted")
		}
		got, err := store.GetSessionForDisplay(row.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		entries := got.Metadata["provider_failures"].([]interface{})
		if len(entries) != MaxProviderFailures || entries[0].(map[string]interface{})["attempt"] != float64(5) {
			t.Fatalf("wrong failure suffix: %v", entries)
		}
		for _, entry := range entries {
			reason := entry.(map[string]interface{})["reason"].(string)
			if len(reason) > MaxProviderFailureTextBytes || !utf8.ValidString(reason) {
				t.Fatal("error body not bounded UTF-8")
			}
		}
		if got.Metadata["provider"] != "test" {
			t.Fatal("display lost metadata")
		}
	}
	if failures[0]["reason"] != strings.Repeat("error 🤖", 1000) {
		t.Fatal("save mutated caller metadata")
	}
	// Stale legacy display projections must never override authoritative metadata.
	if _, err := store.db.Exec(`UPDATE sessions SET display_metadata = '{"provider":"stale"}' WHERE id = ?`, row.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetSessionForDisplay(row.ID, false)
	if err != nil || got.Metadata["provider"] != "test" {
		t.Fatalf("stale display read: %v %v", got, err)
	}
}

func TestCompactSessionMetadataMigrationRollbackAndRestart(t *testing.T) {
	path := t.TempDir()
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	row := compressionTestSession()
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(map[string]interface{}{"provider": "test", "provider_failures": []interface{}{map[string]interface{}{"reason": strings.Repeat("x", 100000)}}})
	if _, err := store.db.Exec(`UPDATE sessions SET metadata = ?, display_metadata = ? WHERE id = ?`, string(legacy), string(legacy), row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE session_metadata_compacted; CREATE TRIGGER reject_compaction BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateCompactSessionMetadata(); err == nil {
		t.Fatal("migration should roll back")
	}
	if exists, err := store.tableExists("session_metadata_compacted"); err != nil || exists {
		t.Fatalf("marker survived rollback: %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_compaction`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		store, err = NewSQLiteStore(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw string
		var display sql.NullString
		if err := store.db.QueryRow(`SELECT metadata, display_metadata FROM sessions WHERE id = ?`, row.ID).Scan(&raw, &display); err != nil {
			t.Fatal(err)
		}
		if display.Valid || len(raw) > 3000 || !strings.Contains(raw, `"provider":"test"`) {
			t.Fatal("legacy metadata not compacted")
		}
		if _, err := store.db.Exec(`CREATE TRIGGER IF NOT EXISTS reject_rewrite BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'unexpected rewrite'); END`); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrationCheckpointAndWALRetentionAfterRestart(t *testing.T) {
	path := t.TempDir()
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	// A second connection keeps the WAL on disk across a simulated restart.
	keeper, err := openSQLiteConnection(store.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer keeper.Close()
	if err := keeper.Ping(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA wal_autocheckpoint=0; CREATE TABLE wal_load (payload BLOB); DROP TABLE session_metadata_compacted`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 70; i++ {
		if _, err := store.db.Exec(`INSERT INTO wal_load VALUES (zeroblob(1048576))`); err != nil {
			t.Fatal(err)
		}
	}
	walPath := store.dbPath + "-wal"
	before, err := os.Stat(walPath)
	if err != nil || before.Size() <= 67108864 {
		t.Fatalf("fixture WAL not large enough: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var limit int64
	for _, db := range []*sql.DB{store.db, store.readDB} {
		if err := db.QueryRow(`PRAGMA journal_size_limit`).Scan(&limit); err != nil || limit != 67108864 {
			t.Fatalf("journal size limit = %d: %v", limit, err)
		}
	}
	peak := int64(0)
	for i := 0; i < 20; i++ {
		info, err := os.Stat(walPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > peak {
			peak = info.Size()
		}
		if info.Size() >= 67108864 {
			t.Fatalf("WAL retained %d bytes after restart", info.Size())
		}
		if _, err := store.db.Exec(`INSERT INTO wal_load VALUES (zeroblob(1048576))`); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("WAL before restart: %d bytes; peak during resumed writes: %d bytes", before.Size(), peak)
}
