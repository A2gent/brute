package agent

import (
	"context"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestUseProviderSessionPersistsCursorAcrossTwoTurns(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	mockLLM := &MockLLM{
		Responses: []*llm.ChatResponse{
			{Content: "hi", ProviderSessionCursor: "cursor-turn-1"},
			{Content: "follow up answer", ProviderSessionCursor: "cursor-turn-2"},
		},
	}
	ag := New(Config{
		MaxSteps:           10,
		UseProviderSession: true,
	}, mockLLM, tools.NewManager(t.TempDir()), sm)

	sess.AddUserMessage("hello")
	if _, _, err := ag.RunWithEvents(context.Background(), sess, "hello", nil); err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}

	if got, _ := sess.Metadata[metadataProviderSessionCursor].(string); got != "cursor-turn-1" {
		t.Fatalf("session metadata provider cursor = %q, want cursor-turn-1", got)
	}
	lastAssistant := lastAssistantMessage(sess)
	if lastAssistant == nil {
		t.Fatal("expected assistant message after turn 1")
	}
	if got, _ := lastAssistant.Metadata[messageMetadataProviderSessionCursor].(string); got != "cursor-turn-1" {
		t.Fatalf("assistant metadata provider cursor = %q, want cursor-turn-1", got)
	}

	reloaded, err := sm.Get(sess.ID)
	if err != nil {
		t.Fatalf("failed to reload session: %v", err)
	}
	if got, _ := reloaded.Metadata[metadataProviderSessionCursor].(string); got != "cursor-turn-1" {
		t.Fatalf("persisted session metadata provider cursor = %q, want cursor-turn-1", got)
	}

	reloaded.AddUserMessage("follow up")
	if _, _, err := ag.RunWithEvents(context.Background(), reloaded, "follow up", nil); err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}

	if len(mockLLM.CapturedRequests) < 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(mockLLM.CapturedRequests))
	}
	secondReq := mockLLM.CapturedRequests[1]
	if secondReq.ProviderSessionCursor != "cursor-turn-1" {
		t.Fatalf("second request ProviderSessionCursor = %q, want cursor-turn-1", secondReq.ProviderSessionCursor)
	}
	if secondReq.PreviousResponseID != "" {
		t.Fatalf("second request PreviousResponseID = %q, want empty", secondReq.PreviousResponseID)
	}
	if len(secondReq.Messages) != 1 || secondReq.Messages[0].Content != "follow up" {
		t.Fatalf("second request messages = %+v, want only latest user message", secondReq.Messages)
	}

	if got, _ := reloaded.Metadata[metadataProviderSessionCursor].(string); got != "cursor-turn-2" {
		t.Fatalf("updated session metadata provider cursor = %q, want cursor-turn-2", got)
	}
}

func lastAssistantMessage(sess *session.Session) *session.Message {
	if sess == nil {
		return nil
	}
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role == "assistant" {
			return &sess.Messages[i]
		}
	}
	return nil
}
