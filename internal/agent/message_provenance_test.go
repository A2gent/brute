package agent

import (
	"context"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"testing"
)

func TestMessageProvenanceSwitchReloadAndFallback(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddAssistantMessage("legacy", nil)
	for _, provider := range []string{"anthropic:work", "openai:personal"} {
		client := &mockStreamingLLM{streamEvents: []llm.StreamEvent{{Type: llm.StreamEventContentDelta, ContentDelta: provider}}, response: &llm.ChatResponse{Content: provider}}
		ag := New(Config{MaxSteps: 3, Provider: provider, Model: "model-" + provider}, client, tools.NewManager(t.TempDir()), sm)
		_, _, err := ag.RunWithEvents(context.Background(), sess, "next", func(ev Event) {
			if ev.Type == EventAssistantDelta && (ev.Provenance == nil || ev.Provenance.Provider != provider) {
				t.Errorf("delta provenance = %+v", ev.Provenance)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := sm.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var providers []string
	for _, msg := range reloaded.Messages {
		if msg.Role == "assistant" {
			if msg.Provenance == nil {
				providers = append(providers, "unknown")
			} else {
				providers = append(providers, msg.Provenance.Provider)
			}
		}
	}
	if len(providers) != 3 || providers[0] != "unknown" || providers[1] != "anthropic:work" || providers[2] != "openai:personal" {
		t.Fatalf("history = %v", providers)
	}
	// The aggregate config must not win over the successful concrete fallback node.
	chain := fallback.NewClient([]fallback.Node{
		{Name: "anthropic:failed", Model: "model-a", Client: &MockLLM{Err: context.DeadlineExceeded}},
		{Name: "openai:backup", Model: "model-b", Client: &mockStreamingLLM{streamEvents: []llm.StreamEvent{{Type: llm.StreamEventContentDelta, ContentDelta: "backup"}}, response: &llm.ChatResponse{Content: "backup"}}},
	}, fallback.WithMaxRetries(0))
	ag := New(Config{MaxSteps: 3, Provider: "fallback_chain:work"}, chain, tools.NewManager(t.TempDir()), sm)
	_, _, err = ag.RunWithEvents(context.Background(), sess, "fallback", func(ev Event) {
		if ev.Type == EventAssistantDelta && (ev.Provenance == nil || ev.Provenance.Provider != "openai:backup" || ev.Provenance.Model != "model-b") {
			t.Errorf("fallback delta = %+v", ev.Provenance)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	last := sess.GetLastMessage()
	if last.Provenance == nil || last.Provenance.Provider != "openai:backup" {
		t.Fatalf("fallback message = %+v", last)
	}
}

func TestMessageProvenanceCompactionPreservesArchivedAndRetainedMessages(t *testing.T) {
	t.Setenv("AAGENT_CONTEXT_COMPACTION_TRIGGER_PERCENT", "50")
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"anthropic:work", "openai:personal"} {
		sess.AddUserMessage("question")
		sess.AddMessage(session.Message{Role: "assistant", Content: provider, Provenance: llm.NewProvenance(provider, "model")})
	}
	ids := []string{sess.Messages[1].ID, sess.Messages[3].ID}
	ag := New(Config{Provider: "openai:personal", Model: "summary-model", ContextWindow: 1000, CompactionTriggerPercent: 50}, &MockLLM{Response: &llm.ChatResponse{Content: "summary"}}, nil, sm)
	metadataSetFloat(sess, metadataCurrentContextTokens, 600)
	_, compacted, err := ag.maybeCompactContext(context.Background(), sess, 1)
	if err != nil || !compacted {
		t.Fatalf("compacted=%v err=%v", compacted, err)
	}
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}
	reloaded, err := sm.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, msg := range reloaded.Messages {
		for i, id := range ids {
			if msg.ID == id {
				want := []string{"anthropic:work", "openai:personal"}[i]
				if msg.Provenance == nil || msg.Provenance.Provider != want {
					t.Fatalf("old message changed: %+v", msg)
				}
				found++
			}
		}
		if msg.Content == "summary" && (msg.Provenance == nil || msg.Provenance.Model != "summary-model") {
			t.Fatalf("summary provenance = %+v", msg.Provenance)
		}
	}
	if found != 2 {
		t.Fatalf("retained %d original messages", found)
	}
}

func TestToolBearingAssistantProvenance(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	client := &MockLLM{Responses: []*llm.ChatResponse{
		{Content: "tool", ToolCalls: []llm.ToolCall{{ID: "t", Name: "missing", Input: "{}"}}},
		{Content: "done"},
	}}
	ag := New(Config{Provider: "anthropic:work", Model: "model", MaxSteps: 3}, client, tools.NewManager(t.TempDir()), sm)
	_, _, err = ag.RunWithEvents(context.Background(), sess, "next", nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range sess.Messages {
		if len(msg.ToolCalls) > 0 {
			found = true
			if msg.Provenance == nil || msg.Provenance.Provider != "anthropic:work" {
				t.Fatalf("tool message = %+v", msg)
			}
		}
	}
	if !found {
		t.Fatal("no tool message")
	}
}
