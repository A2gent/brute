package a2atunnel

import (
	"os"
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestLatestAssistantMessageContentSince(t *testing.T) {
	t.Parallel()

	msgs := []session.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "  "},
		{Role: "assistant", Content: "answer 1"},
		{Role: "user", Content: "next"},
		{Role: "assistant", Content: "answer 2"},
	}
	got := latestAssistantMessageContentSince(msgs, 3)
	if got != "answer 2" {
		t.Fatalf("expected latest assistant message, got %q", got)
	}
	got = latestAssistantMessageContentSince(msgs, 5)
	if got != "" {
		t.Fatalf("expected empty assistant message after start bound, got %q", got)
	}
}

func TestLatestAssistantMessageSincePrefersImages(t *testing.T) {
	t.Parallel()

	msgs := []session.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "", Images: []session.ImageAttachment{{Name: "img-1", URL: "https://example.com/image.png"}}},
	}
	content, images := latestAssistantMessageSince(msgs, 0)
	if content != "" {
		t.Fatalf("expected empty assistant content, got %q", content)
	}
	if len(images) != 1 || images[0].URL != "https://example.com/image.png" {
		t.Fatalf("expected one assistant image, got %#v", images)
	}
}

func TestInboundPromptForRouting(t *testing.T) {
	t.Parallel()

	if got := inboundPromptForRouting(" hello ", 2); got != "hello" {
		t.Fatalf("expected trimmed text prompt, got %q", got)
	}
	if got := inboundPromptForRouting("", 3); got != "[User sent 3 image(s)]" {
		t.Fatalf("expected image routing hint, got %q", got)
	}
	if got := inboundPromptForRouting(" ", 0); got != "" {
		t.Fatalf("expected empty routing prompt, got %q", got)
	}
}

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
	first.AddAssistantMessage("history-should-survive", nil)
	if err := manager.Save(first); err != nil {
		t.Fatalf("save first history failed: %v", err)
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
	if len(second.Messages) == 0 {
		t.Fatalf("expected existing session messages to be loaded, got none")
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

func TestResolveSessionByConversationWithoutSourceAgent(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "a2a-handler-no-source-*")
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
		ConversationID: "conv-only",
	})
	if err != nil {
		t.Fatalf("first resolveSession failed: %v", err)
	}
	first.Metadata[MetaA2AInbound] = true
	first.Metadata[MetaA2ASourceAgentID] = "agent-source-1"
	first.Metadata[MetaA2AConversationID] = "conv-only"
	if err := manager.Save(first); err != nil {
		t.Fatalf("save first failed: %v", err)
	}

	second, err := handler.resolveSession(InboundPayload{
		Task:           "two",
		ConversationID: "conv-only",
	})
	if err != nil {
		t.Fatalf("second resolveSession failed: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected same session ID when only conversation_id is present, got %s vs %s", second.ID, first.ID)
	}
}

func TestResolveSessionDirectByLocalSessionID(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "a2a-handler-local-id-*")
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

	created, err := manager.Create("brute")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	created.AddAssistantMessage("history", nil)
	if err := manager.Save(created); err != nil {
		t.Fatalf("save session: %v", err)
	}

	resolved, err := handler.resolveSession(InboundPayload{
		Task:           "follow-up",
		ConversationID: created.ID,
	})
	if err != nil {
		t.Fatalf("resolveSession failed: %v", err)
	}
	if resolved.ID != created.ID {
		t.Fatalf("expected local session ID reuse, got %s vs %s", resolved.ID, created.ID)
	}
	if len(resolved.Messages) == 0 {
		t.Fatalf("expected existing session messages to be loaded, got none")
	}
}

func TestResolveSessionBySourceSessionIDFallback(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "a2a-handler-source-session-*")
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
		Task:            "one",
		SourceAgentID:   "agent-source-1",
		SourceSessionID: "src-session-1",
	})
	if err != nil {
		t.Fatalf("first resolveSession failed: %v", err)
	}
	first.Metadata[MetaA2AInbound] = true
	first.Metadata[MetaA2ASourceAgentID] = "agent-source-1"
	first.Metadata[MetaA2ASourceSessionID] = "src-session-1"
	if err := manager.Save(first); err != nil {
		t.Fatalf("save first failed: %v", err)
	}

	second, err := handler.resolveSession(InboundPayload{
		Task:            "two",
		SourceSessionID: "src-session-1",
	})
	if err != nil {
		t.Fatalf("second resolveSession failed: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected same session ID when matching source_session_id, got %s vs %s", second.ID, first.ID)
	}
}
