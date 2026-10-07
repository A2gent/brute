package storage

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSessionDisplayProjectionMigrationAndUpdates(t *testing.T) {
	path := t.TempDir()
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	row := &Session{ID: "legacy", AgentID: "build", Status: "idle", CreatedAt: time.Now(), UpdatedAt: time.Now(), Metadata: map[string]interface{}{"provider": "old", "context_compression_store": "private"}}
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	// Reproduce a database created before the display projection existed.
	if _, err := store.db.Exec("ALTER TABLE sessions DROP COLUMN display_metadata"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var raw string
	if err := store.db.QueryRow("SELECT display_metadata FROM sessions WHERE id = ?", row.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var projection map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &projection); err != nil {
		t.Fatal(err)
	}
	if projection["provider"] != "old" || projection["context_compression_store"] != nil {
		t.Fatalf("bad migrated projection: %v", projection)
	}
	row.Metadata = map[string]interface{}{"provider": "new", "archived_at": "2026-10-06T12:00:00Z", "context_compression_store": "still private"}
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	display, err := store.GetSessionForDisplay(row.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if display.Metadata["provider"] != "new" || display.Metadata["archived_at"] != row.Metadata["archived_at"] || display.Metadata["context_compression_store"] != nil {
		t.Fatalf("stale projection: %v", display.Metadata)
	}
	full, err := store.GetSession(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if full.Metadata["context_compression_store"] != "still private" {
		t.Fatal("runtime state lost")
	}
	row.Metadata = nil
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	display, err = store.GetSessionForDisplay(row.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(display.Metadata) != 0 {
		t.Fatalf("removed metadata persisted: %v", display.Metadata)
	}
}

func TestRelatedSessionDisplayReads(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, other, parent, current := "project", "other", "parent", "current"
	for _, row := range []*Session{
		{ID: parent, ProjectID: &project},
		{ID: current, ParentID: &parent, ProjectID: &project},
		{ID: "child", ParentID: &current, ProjectID: &project},
		{ID: "sibling", ParentID: &parent, ProjectID: &project},
		{ID: "other-child", ParentID: &current, ProjectID: &other},
		{ID: "unrelated", ProjectID: &project},
	} {
		row.AgentID, row.Status, row.CreatedAt, row.UpdatedAt = "build", "idle", time.Now(), time.Now()
		if err := store.SaveSession(row); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.ListRelatedSessionsForDisplay(project, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected parent and child, got %d", len(rows))
	}
	for _, row := range rows {
		if row.ID != parent && row.ID != "child" {
			t.Fatalf("unrelated row: %s", row.ID)
		}
	}
	rows, err = store.ListRelatedSessionsForDisplay("", current)
	if err != nil || len(rows) != 3 {
		t.Fatalf("global relations: %d, %v", len(rows), err)
	}
	rows, err = store.ListRelatedSessionsForDisplay(project, "missing")
	if err != nil || len(rows) != 0 {
		t.Fatalf("missing session relations: %d, %v", len(rows), err)
	}
}
