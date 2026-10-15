package storage

import (
	"testing"
	"time"
)

// Rewriting a reloaded message (no monotonic clock) next to an equal-instant
// fresh one (with monotonic clock) must not reorder history.
func TestMessageOrderSurvivesMonotonicClockLoss(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	row := &Session{ID: "order", AgentID: "build", Status: "running", CreatedAt: now, UpdatedAt: now,
		Messages: []Message{
			{ID: "first", Role: "user", Content: "first", Timestamp: now},
			{ID: "second", Role: "assistant", Content: "second", Timestamp: now},
		},
	}
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	row.Messages[1].Timestamp = now.Round(0) // as if reloaded from the database
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetSession(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].ID != "first" || got.Messages[1].ID != "second" {
		t.Fatalf("history reordered: %+v", got.Messages)
	}
}
