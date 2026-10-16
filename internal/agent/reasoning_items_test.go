package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestReasoningItemsSurviveSessionReload(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("reasoning-test")
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"type":"reasoning","id":"rs_test","summary":[],"encrypted_content":"opaque-state"}`)
	mock := &MockLLM{Response: &llm.ChatResponse{Content: "first reply", ReasoningItems: []json.RawMessage{raw}}}
	ag := New(Config{Provider: "openai", MaxSteps: 5}, mock, tools.NewManager(t.TempDir()), sm)
	sess.AddUserMessage("hello")
	if _, _, err := ag.RunWithEvents(t.Context(), sess, "hello", nil); err != nil {
		t.Fatal(err)
	}
	sess, err = session.NewManager(store).Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("continue")
	req := ag.buildRequest(context.Background(), sess)
	found := false
	for _, msg := range req.Messages {
		if len(msg.ReasoningItems) > 0 {
			found = true
			if string(msg.ReasoningItems[0]) != string(raw) {
				t.Fatalf("reasoning = %s", msg.ReasoningItems[0])
			}
		}
	}
	if !found {
		t.Fatal("reasoning state lost after persisted stateless turn")
	}
}

func TestReasoningItemsAreBoundToProviderContextEpoch(t *testing.T) {
	ag := New(Config{Provider: "openai", Model: "new"}, nil, tools.NewManager(t.TempDir()), nil)
	sess := session.New("test")
	sess.Metadata["provider_context_epoch"] = "new-epoch"
	transition, err := session.NewProviderTransition("change", session.ProviderEndpoint{Provider: "openai", Model: "old"}, session.ProviderEndpoint{Provider: "openai", Model: "new"}, "manual", "switch_auto")
	if err != nil {
		t.Fatal(err)
	}
	session.PutProviderTransition(sess, transition)
	raw := json.RawMessage(`{"type":"reasoning","id":"rs_new","summary":[],"encrypted_content":"opaque"}`)
	oldMetadata := map[string]interface{}{messageMetadataReasoningItems: "[" + string(raw) + "]", "reasoning_context_epoch": "old-epoch"}
	newMetadata := map[string]interface{}{}
	preserveReasoningItems(newMetadata, &llm.ChatResponse{ReasoningItems: []json.RawMessage{raw}}, sess)
	sess.AddAssistantMessageWithImagesAndMetadata("old", nil, nil, oldMetadata)
	sess.AddAssistantMessageWithImagesAndMetadata("new", nil, nil, newMetadata)
	req := ag.buildRequest(context.Background(), sess)
	count := 0
	for _, msg := range req.Messages {
		if len(msg.ReasoningItems) > 0 {
			count++
			if msg.Content != "new" {
				t.Fatal("reasoning from old provider context was replayed")
			}
		}
	}
	if count != 1 {
		t.Fatalf("reasoning turns = %d, want one current-epoch turn", count)
	}
}
