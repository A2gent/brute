package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func compressionTestSession() *Session {
	return &Session{ID: "compression", AgentID: "build", Status: "idle", CreatedAt: time.Now(), UpdatedAt: time.Now(), Metadata: map[string]interface{}{"provider": "test"}}
}

func TestSaveSessionExcludesCompressionMetadata(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := compressionTestSession()
	// A value that cannot be marshalled proves Save skips the key before encoding.
	row.Metadata["context_compression_store"] = make(chan int)
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	var metadata string
	var display sql.NullString
	if err := store.db.QueryRow(`SELECT metadata, display_metadata FROM sessions WHERE id = ?`, row.ID).Scan(&metadata, &display); err != nil {
		t.Fatal(err)
	}
	if display.Valid || strings.Contains(metadata, "context_compression_store") || !strings.Contains(metadata, `"provider":"test"`) {
		t.Fatalf("bad metadata: %s; display = %v", metadata, display)
	}
	if row.Metadata["context_compression_store"] == nil {
		t.Fatal("save mutated caller metadata")
	}
}

func TestCompressionEntriesPersistenceAndIsolation(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := compressionTestSession()
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	value := CompressionEntry{Hash: "hash", ToolName: "read", Original: strings.Repeat("original\n", 10000), Compressed: "preview"}
	if err := store.SaveCompressionEntries(row.ID, []CompressionEntry{value}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCompressionEntries(row.ID, []CompressionEntry{value}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.dbPath + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("unchanged entry wrote %d WAL bytes", info.Size())
	}
	if _, err := store.GetCompressionEntry("another", value.Hash); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-session lookup: %v", err)
	}
	value.Compressed = "changed preview"
	if err := store.SaveCompressionEntries(row.ID, []CompressionEntry{value, {Hash: "other", Original: "second"}}); err != nil {
		t.Fatal(err)
	}
	// Stale/full and incremental saves cannot overwrite or delete originals.
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionIncremental(row, nil, nil, func(int) Message { t.Fatal("unexpected message"); return Message{} }); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetCompressionEntry(row.ID, value.Hash)
	if err != nil || loaded.Original != value.Original || loaded.Compressed != value.Compressed {
		t.Fatalf("entry mismatch: %v", err)
	}
	// All ordinary reads work even when the compression table is unavailable.
	if _, err := store.db.Exec("DROP TABLE session_compression_entries"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSession(row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSessionForDisplay(row.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSessionLiveState(row.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListSessions(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListSessionsForDisplay(""); err != nil {
		t.Fatal(err)
	}
}

func TestCompressionMigrationAndRestart(t *testing.T) {
	path := t.TempDir()
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	row := compressionTestSession()
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	payload := map[string]interface{}{"provider": "test", "context_compression_store": map[string]interface{}{
		"legacy-hash": map[string]interface{}{"tool_name": "read", "original": strings.Repeat("original\n", 10000), "compressed": "preview"},
	}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE sessions SET metadata = ?, display_metadata = ? WHERE id = ?`, string(raw), string(raw), row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("DROP TABLE session_compression_entries"); err != nil {
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
		got, err := store.GetCompressionEntry(row.ID, "legacy-hash")
		if err != nil || got.Hash != "legacy-hash" || got.Original != strings.Repeat("original\n", 10000) {
			t.Fatalf("migrated entry mismatch: %v", err)
		}
		for _, column := range []string{"metadata", "display_metadata"} {
			var raw string
			if err := store.db.QueryRow("SELECT "+column+" FROM sessions WHERE id = ?", row.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, "context_compression_store") || !strings.Contains(raw, `"provider":"test"`) {
				t.Fatalf("legacy key retained: %s", column)
			}
		}
		if i == 1 {
			if _, err := store.db.Exec("DELETE FROM sessions WHERE id = ?", row.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetCompressionEntry(row.ID, "legacy-hash"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("cascade failed: %v", err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompressionMigrationRollbackAndInvalidMetadata(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := compressionTestSession()
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	invalid := compressionTestSession()
	invalid.ID = "invalid-json"
	if err := store.SaveSession(invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE sessions SET metadata = '{broken' WHERE id = ?`, invalid.ID); err != nil {
		t.Fatal(err)
	}
	legacy := `{"provider":"test","context_compression_store":{"hash":{"original":"original"}}}`
	if _, err := store.db.Exec(`UPDATE sessions SET metadata = ? WHERE id = ?`, legacy, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("DROP TABLE session_compression_entries"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_compression_migration BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateSessionCompressionEntries(); err == nil {
		t.Fatal("migration should fail")
	}
	exists, err := store.tableExists("session_compression_entries")
	if err != nil || exists {
		t.Fatalf("migration marker survived rollback: %v", err)
	}
	var raw string
	if err := store.db.QueryRow(`SELECT metadata FROM sessions WHERE id = ?`, row.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != legacy {
		t.Fatal("failed migration stripped originals")
	}
	if _, err := store.db.Exec("DROP TRIGGER fail_compression_migration"); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateSessionCompressionEntries(); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT metadata FROM sessions WHERE id = ?`, invalid.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "{broken" {
		t.Fatal("invalid metadata was destroyed")
	}
}
