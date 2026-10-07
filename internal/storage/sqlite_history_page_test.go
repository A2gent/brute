package storage

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestSessionHistoryPagesStableAtEqualTimestamps(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	row := &Session{ID: "history", AgentID: "build", Status: "completed", CreatedAt: now, UpdatedAt: now}
	for i := 0; i < 9; i++ {
		row.Messages = append(row.Messages, Message{ID: fmt.Sprintf("%02d", 8-i), Role: "user", Content: "payload", Timestamp: now})
	}
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	var ids []string
	cursor := ""
	for {
		page, more, err := store.GetSessionPageForDisplay(row.ID, 3, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Messages) > 3 {
			t.Fatal("unbounded page")
		}
		batch := []string{}
		for _, msg := range page.Messages {
			batch = append(batch, msg.ID)
		}
		ids = append(batch, ids...)
		if !more {
			break
		}
		cursor = page.Messages[0].ID
	}
	if fmt.Sprint(ids) != "[08 07 06 05 04 03 02 01 00]" {
		t.Fatalf("lost or duplicate messages: %v", ids)
	}
	if _, _, err := store.GetSessionPageForDisplay(row.ID, 3, "missing"); !errors.Is(err, ErrMessageCursorMissing) {
		t.Fatalf("missing cursor: %v", err)
	}
	// A cursor from another session must never expose its timestamp or history.
	foreign := &Session{ID: "foreign", AgentID: "build", Status: "completed", CreatedAt: now, UpdatedAt: now, Messages: []Message{{ID: "foreign-message", Role: "user", Timestamp: now}}}
	if err := store.SaveSession(foreign); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetSessionPageForDisplay(row.ID, 3, "foreign-message"); !errors.Is(err, ErrMessageCursorMissing) {
		t.Fatalf("foreign cursor accepted: %v", err)
	}
	row.Status = "running"
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	page, more, err := store.GetSessionPageForDisplay(row.ID, 3, "")
	if err != nil || more || len(page.Messages) != 9 {
		t.Fatalf("live history was truncated: %d %v %v", len(page.Messages), more, err)
	}
}

func TestIncrementalSaveMaterializesOnlyDirtyNewAndTailMessages(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	row := &Session{ID: "incremental", AgentID: "build", Status: "running", CreatedAt: now, UpdatedAt: now}
	for i := 0; i < 10; i++ {
		row.Messages = append(row.Messages, Message{ID: fmt.Sprintf("msg-%d", i), Role: "user", Content: "original", Timestamp: now.Add(time.Duration(i) * time.Second)})
	}
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	row.Messages[0].Content = "edited"
	ids := make([]string, len(row.Messages))
	for i, msg := range row.Messages {
		ids[i] = msg.ID
	}
	var materialized []int
	if err := store.SaveSessionIncremental(row, ids, map[string]bool{ids[0]: true}, func(i int) Message { materialized = append(materialized, i); return row.Messages[i] }); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(materialized) != "[0 6 7 8 9]" {
		t.Fatalf("unnecessary serialization: %v", materialized)
	}
	loaded, err := store.GetSession(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Messages[0].Content != "edited" {
		t.Fatal("historical edit lost")
	}
	// Removing messages must reconcile membership, even when old rows are skipped.
	row.Messages = row.Messages[2:]
	ids = ids[2:]
	if err := store.SaveSessionIncremental(row, ids, nil, func(i int) Message { return row.Messages[i] }); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.GetSession(row.ID)
	if err != nil || len(loaded.Messages) != 8 {
		t.Fatalf("removals lost: %v", err)
	}
	// Ordinary full saves must persist edits anywhere, rather than only in the tail.
	row.Messages[0].Content = "full edit"
	if err := store.SaveSession(row); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.GetSession(row.ID)
	if err != nil || loaded.Messages[0].Content != "full edit" {
		t.Fatal("full save lost an older edit")
	}
}
