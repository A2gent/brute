package storage

import (
	"fmt"
	"testing"
	"time"
)

// Worktree slots have no cap: finished sessions that still own a worktree must not
// block new ones, and the lowest freed slot is reused.
func TestCreateSessionWorktreeSlotsAreUnboundedAndReused(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	now := time.Now()
	if err := store.SaveProject(&Project{ID: "p", Name: "p", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("save project: %v", err)
	}
	create := func(id string) int {
		t.Helper()
		if err := store.SaveSession(&Session{ID: id, AgentID: "build", Status: "completed", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("save session: %v", err)
		}
		wt := &SessionWorktree{SessionID: id, ProjectID: "p", Path: "/tmp/" + id, Branch: id, CreatedAt: time.Now()}
		if err := store.CreateSessionWorktree(wt); err != nil {
			t.Fatalf("create worktree %s: %v", id, err)
		}
		return wt.Slot
	}

	for i := 1; i <= 12; i++ {
		if got := create(fmt.Sprintf("s%d", i)); got != i {
			t.Fatalf("session %d got slot %d", i, got)
		}
	}
	if err := store.DeleteSessionWorktree("s3"); err != nil {
		t.Fatalf("delete worktree: %v", err)
	}
	if got := create("s13"); got != 3 {
		t.Fatalf("freed slot not reused, got %d want 3", got)
	}
}
