package session

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

type liveStateOnlyStore struct {
	storage.Store
	row    *storage.Session
	cursor string
}

func (s *liveStateOnlyStore) GetSession(string) (*storage.Session, error) {
	return nil, errors.New("full transcript read is forbidden")
}

func (s *liveStateOnlyStore) GetSessionLiveState(id, cursor string) (*storage.Session, error) {
	s.cursor = cursor
	return s.row, nil
}

func TestManagerGetLiveStateUsesLightweightStore(t *testing.T) {
	store := &liveStateOnlyStore{row: &storage.Session{ID: "session", Status: "running", Messages: []storage.Message{{ID: "new", Role: "tool", ToolResults: json.RawMessage(`[{"tool_call_id":"call","content":"result"}]`)}}}}
	live, err := NewManager(store).GetLiveState("session", "tail")
	if err != nil {
		t.Fatal(err)
	}
	if store.cursor != "tail" || live.Status != StatusRunning || live.Messages[0].ToolResults[0].Content != "result" {
		t.Fatalf("bad live state: %#v", live)
	}
}

func TestSaveMessageMetadataKeyPreservesConcurrentState(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store)
	sess := New("build")
	sess.AddAssistantMessageWithMetadata("", []ToolCall{{ID: "tool"}}, map[string]interface{}{"timing": "keep"})
	messageID := sess.Messages[0].ID
	if err := manager.Save(sess); err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessageWithImagesAndMetadata("note", []ImageAttachment{{URL: "https://example.com/note.png"}}, map[string]interface{}{"injected_during_run": true})
	sess.Status = StatusInputRequired
	sess.TaskProgress = "new plan"
	sess.Metadata = map[string]interface{}{"approval_audit": "fresh"}
	if err := manager.Save(sess); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveMessageMetadataKey(sess.ID, messageID, "pending_tool_results", []ToolResult{{ToolCallID: "tool", Content: "latest"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	live, err := manager.GetLiveState(sess.ID, messageID)
	if err != nil || len(live.Messages) != 1 || len(live.Messages[0].Images) != 1 {
		t.Fatalf("note/image not returned: %#v, %v", live, err)
	}
	fresh, err := manager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != StatusInputRequired || fresh.TaskProgress != "new plan" || fresh.Metadata["approval_audit"] != "fresh" || len(fresh.Messages) != 2 || fresh.Messages[0].Metadata["timing"] != "keep" || fresh.Messages[0].Metadata["pending_tool_results"] == nil {
		t.Fatalf("concurrent fields overwritten: %#v", fresh)
	}
}

func TestManagerGetLiveStateFallbackFiltersWithoutChangingStore(t *testing.T) {
	store := newMemStore()
	manager := NewManager(store)
	sess := New("build")
	sess.AddUserMessageWithImagesAndMetadata("old note", nil, map[string]interface{}{"injected_during_run": true})
	sess.AddAssistantMessage("known tail", nil)
	tail := sess.Messages[1].ID
	sess.AddAssistantMessage("new suffix", nil)
	if err := manager.Save(sess); err != nil {
		t.Fatal(err)
	}
	live, err := manager.GetLiveState(sess.ID, tail)
	if err != nil || len(live.Messages) != 2 || live.Messages[0].Content != "old note" || live.Messages[1].Content != "new suffix" {
		t.Fatalf("bad fallback: %#v, %v", live, err)
	}
	live, err = manager.GetLiveState(sess.ID, "unsaved")
	if err != nil || len(live.Messages) != 1 || live.Messages[0].Content != "old note" {
		t.Fatalf("missing cursor fallback: %#v, %v", live, err)
	}
	full, err := manager.Get(sess.ID)
	if err != nil || len(full.Messages) != 3 {
		t.Fatal("fallback truncated stored transcript")
	}
}
