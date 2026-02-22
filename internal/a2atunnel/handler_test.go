package a2atunnel

import (
	"os"
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestResolveSessionByConversationContinuity(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "a2a-handler-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := storage.NewSQLiteStore(tempDir)
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	defer store.Close()

	manager := session.NewManager(store)
	handler := &InboundHandler{
		agentID:        "brute",
		sessionManager: manager,
	}

	first, err := handler.resolveSession(InboundPayload{
		Task:           "one",
		SourceAgentID:  "agent-source-1",
		ConversationID: "conv-1",
	})
	if err != nil {
		t.Fatalf("first resolveSession failed: %v", err)
	}
	first.Metadata[MetaA2AInbound] = true
	first.Metadata[MetaA2ASourceAgentID] = "agent-source-1"
	first.Metadata[MetaA2AConversationID] = "conv-1"
	if err := manager.Save(first); err != nil {
		t.Fatalf("save first failed: %v", err)
	}

	second, err := handler.resolveSession(InboundPayload{
		Task:           "two",
		SourceAgentID:  "agent-source-1",
		ConversationID: "conv-1",
	})
	if err != nil {
		t.Fatalf("second resolveSession failed: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected same session ID for same source+conversation, got %s vs %s", second.ID, first.ID)
	}

	third, err := handler.resolveSession(InboundPayload{
		Task:           "three",
		SourceAgentID:  "agent-source-1",
		ConversationID: "conv-2",
	})
	if err != nil {
		t.Fatalf("third resolveSession failed: %v", err)
	}
	if third.ID == first.ID {
		t.Fatalf("expected different session for different conversation, got same %s", third.ID)
	}
}
