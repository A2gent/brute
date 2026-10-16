package storage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSystemPromptSnapshotMigrationAndRestart(t *testing.T) {
	path := t.TempDir()
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	// Preserve escaped newlines, Unicode, legacy timestamps and the frozen prefix.
	prefix := "Environment context:\n- Current time: 2026-10-14T18:33:24+03:00 (Local)\n<legacy> 🤖\n" + strings.Repeat("frozen\n", 10000)
	payload, _ := json.Marshal(map[string]interface{}{"combined_prompt": prefix, "blocks": []interface{}{}})
	legacy, _ := json.Marshal(map[string]interface{}{"provider": "test", SystemPromptSnapshotMetadataKey: json.RawMessage(payload)})
	for _, id := range []string{"full", "display-only", "invalid-metadata", "invalid-display", "null-metadata", "no-snapshot"} {
		row := compressionTestSession()
		row.ID = id
		if err := store.SaveSession(row); err != nil {
			t.Fatal(err)
		}
		metadata, display := string(legacy), string(legacy)
		switch id {
		case "full":
			display = `{"provider":"test","system_prompt_snapshot":{"combined_prompt":"stale display"}}`
		case "display-only":
			metadata = `{"provider":"test"}`
		case "invalid-metadata":
			metadata = `not json`
		case "invalid-display":
			display = `not json`
		case "null-metadata":
			metadata = `{"provider":"test","system_prompt_snapshot":null}`
		case "no-snapshot":
			metadata, display = `not json`, `{"provider":"test"}`
		}
		if _, err := store.db.Exec(`UPDATE sessions SET metadata = ?, display_metadata = ? WHERE id = ?`, metadata, display, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec("DROP TABLE session_system_prompt_snapshots"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for restart := 0; restart < 2; restart++ {
		store, err = NewSQLiteStore(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"full", "display-only", "invalid-metadata", "invalid-display", "null-metadata"} {
			got, err := store.GetSystemPromptSnapshot(id)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				CombinedPrompt string `json:"combined_prompt"`
			}
			if err := json.Unmarshal(got, &decoded); err != nil || decoded.CombinedPrompt != prefix {
				t.Fatalf("%s: frozen prefix changed after migration/restart: %v", id, err)
			}
			var metadata, display string
			if err := store.db.QueryRow(`SELECT metadata, display_metadata FROM sessions WHERE id = ?`, id).Scan(&metadata, &display); err != nil {
				t.Fatal(err)
			}
			for _, raw := range []string{metadata, display} {
				if strings.Contains(raw, SystemPromptSnapshotMetadataKey) {
					t.Fatalf("snapshot not stripped from %s", id)
				}
				if raw != "not json" && !strings.Contains(raw, `"provider":"test"`) {
					t.Fatalf("unrelated metadata lost: %s", raw)
				}
			}
		}
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM session_system_prompt_snapshots`).Scan(&count); err != nil || count != 5 {
			t.Fatalf("unexpected snapshot rows: %d %v", count, err)
		}
		// The table marker must make later runs read-only, even with writes forbidden.
		if _, err := store.db.Exec(`CREATE TRIGGER IF NOT EXISTS reject_snapshot_rewrite BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'unexpected rewrite'); END`); err != nil {
			t.Fatal(err)
		}
		if err := store.migrateSessionSystemPromptSnapshots(); err != nil {
			t.Fatalf("migration repeated: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSystemPromptSnapshotMigrationRollback(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := compressionTestSession()
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	legacy := `{"provider":"test","system_prompt_snapshot":{"combined_prompt":"frozen prefix"}}`
	if _, err := store.db.Exec(`UPDATE sessions SET metadata = ?, display_metadata = ? WHERE id = ?`, legacy, legacy, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE session_system_prompt_snapshots;
		CREATE TRIGGER reject_snapshot_strip BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'stop migration'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateSessionSystemPromptSnapshots(); err == nil {
		t.Fatal("expected strip failure")
	}
	if exists, err := store.tableExists("session_system_prompt_snapshots"); err != nil || exists {
		t.Fatalf("table marker survived rollback: %v %v", exists, err)
	}
	var metadata, display string
	if err := store.db.QueryRow(`SELECT metadata, display_metadata FROM sessions WHERE id = ?`, row.ID).Scan(&metadata, &display); err != nil || metadata != legacy || display != legacy {
		t.Fatalf("legacy snapshots lost on rollback: %s %s %v", metadata, display, err)
	}
	if _, err := store.db.Exec("DROP TRIGGER reject_snapshot_strip"); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateSessionSystemPromptSnapshots(); err != nil {
		t.Fatal(err)
	}
	assertSnapshotMetadataRemoved(t, store, row.ID)
}
