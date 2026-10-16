package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSystemPromptSnapshotPersistenceAndLazyReads(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	row := compressionTestSession()
	// Display copies must be excluded before marshalling, not merely from SQL reads.
	row.Metadata[SystemPromptSnapshotMetadataKey] = make(chan int)
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	if row.Metadata[SystemPromptSnapshotMetadataKey] == nil {
		t.Fatal("save mutated caller metadata")
	}
	assertSnapshotMetadataRemoved(t, store, row.ID)
	payload := json.RawMessage(`{"base_prompt":"frozen","combined_prompt":"Environment context:\n- Current time: old"}`)
	if err := store.SaveSystemPromptSnapshot(row.ID, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSystemPromptSnapshot("other"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-session lookup: %v", err)
	}
	if err := store.SaveSystemPromptSnapshot(row.ID, json.RawMessage(`invalid`)); err == nil {
		t.Fatal("accepted invalid JSON")
	}
	if _, err := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSystemPromptSnapshot(row.ID, payload); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.dbPath + "-wal")
	if err != nil || info.Size() != 0 {
		t.Fatalf("identical snapshot dirtied WAL: %v %v", info, err)
	}
	changed := json.RawMessage(`{"combined_prompt":"changed"}`)
	if err := store.SaveSystemPromptSnapshot(row.ID, changed); err != nil {
		t.Fatal(err)
	}
	// Neither stale display metadata nor absence of the key can change a snapshot.
	row.Metadata[SystemPromptSnapshotMetadataKey] = map[string]interface{}{"combined_prompt": "stale"}
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	delete(row.Metadata, SystemPromptSnapshotMetadataKey)
	if err := store.SaveSessionIncremental(row, nil, nil, func(int) Message {
		t.Fatal("unexpected message")
		return Message{}
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetSystemPromptSnapshot(row.ID)
	if err != nil || string(got) != string(changed) {
		t.Fatalf("snapshot changed by ordinary save: %s %v", got, err)
	}

	// If ordinary reads tried to hydrate snapshots, all of these would fail.
	if _, err := store.db.Exec("DROP TABLE session_system_prompt_snapshots"); err != nil {
		t.Fatal(err)
	}
	for name, read := range map[string]func() (*Session, error){
		"full":    func() (*Session, error) { return store.GetSession(row.ID) },
		"display": func() (*Session, error) { return store.GetSessionForDisplay(row.ID, true) },
		"summary": func() (*Session, error) { return store.GetSessionSummary(row.ID) },
		"live":    func() (*Session, error) { return store.GetSessionLiveState(row.ID, "") },
	} {
		got, err := read()
		if err != nil || got.Metadata[SystemPromptSnapshotMetadataKey] != nil {
			t.Fatalf("%s read loaded snapshot: %+v %v", name, got, err)
		}
	}
	if _, err := store.ListSessions(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListSessionsForDisplay(""); err != nil {
		t.Fatal(err)
	}
}

func assertSnapshotMetadataRemoved(t *testing.T, store *SQLiteStore, id string) {
	t.Helper()
	var metadata, display string
	if err := store.db.QueryRow(`SELECT metadata, display_metadata FROM sessions WHERE id = ?`, id).Scan(&metadata, &display); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{metadata, display} {
		if strings.Contains(raw, SystemPromptSnapshotMetadataKey) || !strings.Contains(raw, `"provider":"test"`) {
			t.Fatalf("incorrect metadata projection: %s", raw)
		}
	}
}

func TestSystemPromptSnapshotCopyIsAtomic(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := compressionTestSession()
	if err := store.SaveSession(source); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"combined_prompt":"frozen prefix"}`)
	if err := store.SaveSystemPromptSnapshot(source.ID, payload); err != nil {
		t.Fatal(err)
	}
	child := compressionTestSession()
	child.ID, child.SystemPromptSnapshotSourceID = "child", source.ID
	// Failure after both the child and snapshot inserts must roll back both.
	if err := store.SaveSessionIncremental(child, []string{"expected"}, nil, func(int) Message { return Message{ID: "wrong"} }); err == nil {
		t.Fatal("expected message failure")
	}
	if _, err := store.GetSession(child.ID); err == nil {
		t.Fatal("child survived rollback")
	}
	if _, err := store.GetSystemPromptSnapshot(child.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("snapshot survived rollback: %v", err)
	}
	if err := store.SaveSession(child); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetSystemPromptSnapshot(child.ID)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("snapshot not copied: %s %v", got, err)
	}
	if err := store.SaveSystemPromptSnapshot(source.ID, json.RawMessage(`{"combined_prompt":"new source"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(child); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetSystemPromptSnapshot(child.ID)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("retry overwrote child prefix: %s %v", got, err)
	}
	if err := store.DeleteSession(source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSystemPromptSnapshot(source.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("source cascade failed: %v", err)
	}
	if _, err := store.GetSystemPromptSnapshot(child.ID); err != nil {
		t.Fatalf("deleting source removed independent child snapshot: %v", err)
	}
}
