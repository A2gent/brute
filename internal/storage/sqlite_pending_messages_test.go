package storage

import (
	"testing"
	"time"
)

func TestPendingMessagesAreFIFOAndSurviveSessionSaves(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := &Session{ID: "s1", AgentID: "build", Status: "running", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.SaveSession(sess); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := store.AddPendingMessage(&PendingMessage{ID: id, SessionID: "s1", Content: id, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	// A running agent re-saves its stale in-memory session; queued rows must be untouched.
	if err := store.SaveSession(sess); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListPendingMessages("s1")
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %v, err = %v", list, err)
	}
	if got, _ := store.TakePendingMessage("s1", "b"); got == nil || got.ID != "b" {
		t.Fatalf("take b = %v", got)
	}
	if got, _ := store.TakePendingMessage("s1", "b"); got != nil {
		t.Fatalf("second take of b should be nil, got %v", got)
	}
	next, _ := store.TakeNextPendingMessage("s1")
	if next == nil || next.ID != "a" {
		t.Fatalf("next = %v, want a", next)
	}
	next, _ = store.TakeNextPendingMessage("s1")
	if next == nil || next.ID != "c" {
		t.Fatalf("next = %v, want c", next)
	}
	if next, _ = store.TakeNextPendingMessage("s1"); next != nil {
		t.Fatalf("queue should be empty, got %v", next)
	}
}
