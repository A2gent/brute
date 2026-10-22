package agent

import (
	"context"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestCompactContextOnDemandDoesNotResumeAgent(t *testing.T) {
	t.Setenv(envCompactionTriggerPercent, "")
	t.Setenv(envCompactionPrompt, "")
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("original task")
	sess.AddAssistantMessage("first response", nil)
	sess.AddUserMessage("follow-up task")
	sess.AddAssistantMessage("latest response", nil)
	metadataSetFloat(sess, metadataCurrentContextTokens, 100)
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}

	client := &MockLLM{Response: &llm.ChatResponse{Content: "Compacted summary"}}
	ag := New(Config{
		Provider:                 "openai",
		Model:                    "test-model",
		ContextWindow:            1000,
		CompactionTriggerPercent: 90,
		CompactionPrompt:         "Summarize the conversation",
	}, client, nil, sm)

	if err := ag.CompactContext(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if client.CapturedRequest == nil {
		t.Fatal("manual compaction did not call the summarizer")
	}
	if len(sess.Messages) != 5 {
		t.Fatalf("manual compaction should add one summary only, got %d messages", len(sess.Messages))
	}
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != "assistant" || last.Content != "latest response" {
		t.Fatalf("manual compaction should not append a continuation prompt: %+v", last)
	}
	if got := metadataFloat(sess.Metadata, metadataCompactionCount); got != 1 {
		t.Fatalf("compaction count = %.0f, want 1", got)
	}
	if got := metadataFloat(sess.Metadata, metadataCurrentContextTokens); got != 0 {
		t.Fatalf("current context tokens = %.0f, want 0", got)
	}
}
