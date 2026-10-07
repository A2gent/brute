package session

import (
	"fmt"
	"github.com/A2gent/brute/internal/storage"
	"strings"
	"testing"
)

func TestManagerPersistsChangedHistoricalToolPayload(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store)
	sess := New("build")
	for i := 0; i < 10; i++ {
		sess.AddToolResult([]ToolResult{{ToolCallID: fmt.Sprint(i), Content: "original"}})
	}
	if err := manager.Save(sess); err != nil {
		t.Fatal(err)
	}
	sess.Messages[0].ToolResults[0].Content = "repaired reference"
	beforeEdit := sess.UpdatedAt
	sess.MarkMessageChanged(sess.Messages[0].ID)
	if !sess.UpdatedAt.After(beforeEdit) {
		t.Fatal("historical edit did not advance transcript version")
	}
	if err := manager.Save(sess); err != nil {
		t.Fatal(err)
	}
	restored, err := manager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Messages[0].ToolResults[0].Content != "repaired reference" {
		t.Fatal("historical tool repair was lost")
	}
	if len(sess.dirtyMessageIDs) != 0 {
		t.Fatal("dirty state not cleared after commit")
	}
	// Compaction can insert a new message before the mutable tail.
	restored.Messages = append([]Message{{ID: "inserted", Role: "assistant", Content: "compaction summary", Timestamp: restored.CreatedAt}}, restored.Messages...)
	if err := manager.Save(restored); err != nil {
		t.Fatal(err)
	}
	full, err := manager.Get(sess.ID)
	if err != nil || len(full.Messages) != 11 {
		t.Fatalf("inserted history lost: %v", err)
	}
}

func BenchmarkSessionSaveSerialization(b *testing.B) {
	store, err := storage.NewSQLiteStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store)
	sess := New("build")
	for i := 0; i < 500; i++ {
		sess.AddToolResult([]ToolResult{{ToolCallID: fmt.Sprint(i), Content: strings.Repeat("x", 16<<10)}})
	}
	if err := manager.Save(sess); err != nil {
		b.Fatal(err)
	}
	ids := make([]string, len(sess.Messages))
	for i, msg := range sess.Messages {
		ids[i] = msg.ID
	}
	b.Run("eager_serialization", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			row := sess.ToStorage()
			if err := store.SaveSessionIncremental(row, ids, nil, func(j int) storage.Message { return row.Messages[j] }); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("incremental_serialization", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := manager.Save(sess); err != nil {
				b.Fatal(err)
			}
		}
	})
}
