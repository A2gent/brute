package storage

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGetSessionLiveState(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	row := &Session{ID: "live", AgentID: "build", Status: "input_required", TaskProgress: "plan", CreatedAt: now, UpdatedAt: now,
		Metadata: map[string]interface{}{"approval_audit": "fresh", "context_compression_store": "originals"},
		Messages: []Message{
			{ID: "old", Role: "tool", ToolResults: json.RawMessage(`[{"content":"old payload"}]`), Timestamp: now},
			{ID: "note", Role: "user", Content: "injected before tail", Metadata: map[string]interface{}{"injected_during_run": true}, Timestamp: now},
			{ID: "tail", Role: "assistant", Timestamp: now},
			{ID: "new", Role: "tool", ToolResults: json.RawMessage(`[{"content":"new payload"}]`), Timestamp: now},
			{ID: "string-note", Role: "user", Metadata: map[string]interface{}{"injected_during_run": " TRUE "}, Timestamp: now},
		},
	}
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cursor string
		ids    []string
	}{
		{"tail", []string{"note", "new", "string-note"}},
		{"new", []string{"note", "string-note"}},
		{"unsaved-tail", []string{"note", "string-note"}},
		{"", []string{"old", "note", "tail", "new", "string-note"}},
	} {
		t.Run(tc.cursor, func(t *testing.T) {
			live, err := store.GetSessionLiveState(row.ID, tc.cursor)
			if err != nil {
				t.Fatal(err)
			}
			if live.Status != row.Status || live.TaskProgress != row.TaskProgress || !live.UpdatedAt.Equal(now) || live.Metadata["approval_audit"] != "fresh" || live.Metadata["context_compression_store"] != nil {
				t.Fatalf("incomplete live state: %#v", live)
			}
			if len(live.Messages) != len(tc.ids) {
				t.Fatalf("got %d messages, want %v", len(live.Messages), tc.ids)
			}
			for i, id := range tc.ids {
				if live.Messages[i].ID != id {
					t.Fatalf("message %d = %q, want %q", i, live.Messages[i].ID, id)
				}
			}
		})
	}
	if _, err := store.GetSessionLiveState("missing", "tail"); err == nil {
		t.Fatal("missing session should fail")
	}
	// Reopening runs the idempotent index migration on existing transcripts.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(store.dataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	live, err := store.GetSessionLiveState(row.ID, "tail")
	if err != nil || len(live.Messages) != 3 {
		t.Fatalf("live state after reopen: %v, %v", live, err)
	}
}
