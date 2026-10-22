package agent

import (
	"context"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestAddTokenUsageMetadataTracksBilledTotals(t *testing.T) {
	ag := New(Config{ContextWindow: 200000}, nil, nil, nil)
	sess := session.New("build")

	// Request 1: first request bills the full input, 400 of it served from cache.
	ag.addTokenUsageMetadata(sess, llm.TokenUsage{InputTokens: 1000, CachedInputTokens: 400, OutputTokens: 100, ReasoningTokens: 20})
	// Request 2: context grew by 250 tokens, but billing counts the full input again.
	ag.addTokenUsageMetadata(sess, llm.TokenUsage{InputTokens: 1250, CachedInputTokens: 900, OutputTokens: 80, ReasoningTokens: 10})

	// Billed totals sum raw per-request input (real spend / cache hit rate).
	if got := metadataFloat(sess.Metadata, metadataBilledInputTokens); got != 2250 {
		t.Fatalf("billed_input_tokens = %v, want 2250", got)
	}
	if got := metadataFloat(sess.Metadata, metadataBilledCachedInputTokens); got != 1300 {
		t.Fatalf("billed_cached_input_tokens = %v, want 1300", got)
	}

	// Legacy fields keep their old context-growth semantics for compatibility.
	if got := metadataFloat(sess.Metadata, metadataTotalInputTokens); got != 1250 {
		t.Fatalf("total_input_tokens = %v, want 1250 (context growth)", got)
	}
	if got := metadataFloat(sess.Metadata, metadataTotalOutputTokens); got != 180 {
		t.Fatalf("total_output_tokens = %v, want 180", got)
	}
	if got := metadataFloat(sess.Metadata, metadataTotalCachedTokens); got != 1300 {
		t.Fatalf("total_cached_input_tokens = %v, want 1300", got)
	}
	if got := metadataFloat(sess.Metadata, metadataTotalReasoningTokens); got != 30 {
		t.Fatalf("total_reasoning_tokens = %v, want 30", got)
	}
	if got := metadataFloat(sess.Metadata, metadataCurrentContextTokens); got != 1250 {
		t.Fatalf("current_context_tokens = %v, want 1250", got)
	}
}

func TestLoopPersistsPerRequestLLMUsageOnAssistantMessages(t *testing.T) {
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
	sess.AddUserMessage("run progress tool")

	mockLLM := &MockLLM{
		Responses: []*llm.ChatResponse{
			{
				ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "progress_test", Input: `{}`}},
				Usage:     llm.TokenUsage{InputTokens: 1000, CachedInputTokens: 400, OutputTokens: 50, ReasoningTokens: 10},
			},
			{
				Content: "done",
				Usage:   llm.TokenUsage{InputTokens: 1250, CachedInputTokens: 900, OutputTokens: 30, ReasoningTokens: 5},
			},
		},
	}
	manager := tools.NewManager(t.TempDir())
	manager.Register(loopProgressTool{})
	ag := New(Config{MaxSteps: 3}, mockLLM, manager, sm)

	if _, _, err := ag.RunWithEvents(context.Background(), sess, "run progress tool", nil); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	var usages []map[string]interface{}
	for _, msg := range sess.Messages {
		if msg.Role != "assistant" {
			continue
		}
		raw, ok := msg.Metadata[messageMetadataLLMUsage]
		if !ok {
			t.Fatalf("assistant message %s missing %s metadata", msg.ID, messageMetadataLLMUsage)
		}
		usage, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("%s metadata is %#v, want map", messageMetadataLLMUsage, raw)
		}
		usages = append(usages, usage)
	}
	if len(usages) != 2 {
		t.Fatalf("assistant messages with %s = %d, want 2", messageMetadataLLMUsage, len(usages))
	}

	// Per-request billed counts must survive on each message individually.
	if got := metadataFloat(usages[0], "input_tokens"); got != 1000 {
		t.Fatalf("request 1 input_tokens = %v, want 1000", got)
	}
	if got := metadataFloat(usages[0], "cached_input_tokens"); got != 400 {
		t.Fatalf("request 1 cached_input_tokens = %v, want 400", got)
	}
	if got := metadataFloat(usages[1], "input_tokens"); got != 1250 {
		t.Fatalf("request 2 input_tokens = %v, want 1250", got)
	}
	if got := metadataFloat(usages[1], "cached_input_tokens"); got != 900 {
		t.Fatalf("request 2 cached_input_tokens = %v, want 900", got)
	}
	if got := metadataFloat(usages[1], "output_tokens"); got != 30 {
		t.Fatalf("request 2 output_tokens = %v, want 30", got)
	}
	if got := metadataFloat(usages[1], "reasoning_tokens"); got != 5 {
		t.Fatalf("request 2 reasoning_tokens = %v, want 5", got)
	}

	// Session totals aggregate the same raw counts.
	if got := metadataFloat(sess.Metadata, metadataBilledInputTokens); got != 2250 {
		t.Fatalf("billed_input_tokens = %v, want 2250", got)
	}
	if got := metadataFloat(sess.Metadata, metadataBilledCachedInputTokens); got != 1300 {
		t.Fatalf("billed_cached_input_tokens = %v, want 1300", got)
	}
}
