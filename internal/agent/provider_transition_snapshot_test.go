package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestProviderTransitionPreservesStoredSystemPromptSnapshot(t *testing.T) {
	for _, mode := range []string{"switch_auto", "compact_auto", "linked_session"} {
		t.Run(mode, func(t *testing.T) {
			path := t.TempDir()
			store, err := storage.NewSQLiteStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { store.Close() }()
			sm := session.NewManager(store)
			source, err := sm.Create("build")
			if err != nil {
				t.Fatal(err)
			}
			payload := json.RawMessage(`{"base_prompt":"legacy","combined_prompt":"Environment context:\n- Current time: frozen 🤖"}`)
			if err := store.SaveSystemPromptSnapshot(source.ID, payload); err != nil {
				t.Fatal(err)
			}
			source.AddUserMessage("task")
			source.AddAssistantMessage("completed work", nil)
			source.AddUserMessage("continue")
			tr, err := session.NewProviderTransition("frozen-transition", session.ProviderEndpoint{Provider: "source", Model: "old"}, session.ProviderEndpoint{Provider: "target", Model: "new"}, "manual", mode)
			if err != nil {
				t.Fatal(err)
			}
			session.PutProviderTransition(source, tr)
			if err := sm.Save(source); err != nil {
				t.Fatal(err)
			}
			// Simulate a restart: transitions must not rely on a metadata copy.
			source, err = sm.Get(source.ID)
			if err != nil {
				t.Fatal(err)
			}
			if source.Metadata[storage.SystemPromptSnapshotMetadataKey] != nil {
				t.Fatal("transition eagerly loaded snapshot")
			}
			target := &MockLLM{Response: &llm.ChatResponse{Content: "summary"}}
			a := New(Config{Model: "old", SystemPrompt: "Environment context:\nfrozen",
				ResolveTransitionTarget: func(*session.Session, session.ProviderEndpoint) (llm.Client, int, error) {
					return target, 65536, nil
				}}, target, tools.NewManager(t.TempDir()), sm)
			err = a.transitionBoundary(context.Background(), source, nil)
			id := source.ID
			if mode == "linked_session" {
				if !errors.Is(err, ErrTransitionPaused) {
					t.Fatalf("linked transition: %v", err)
				}
				id = session.CurrentProviderTransition(source).LinkedSessionID
				// A source retry after child-save must reuse the child and its prefix.
				replay := session.CurrentProviderTransition(source)
				if err := a.linkTransition(source, source, replay, nil); !errors.Is(err, ErrTransitionPaused) {
					t.Fatalf("linked replay: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = storage.NewSQLiteStore(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, sessionID := range []string{source.ID, id} {
				got, err := store.GetSystemPromptSnapshot(sessionID)
				if err != nil || string(got) != string(payload) {
					t.Fatalf("transition/restart changed frozen prefix: %s %v", got, err)
				}
				row, err := store.GetSession(sessionID)
				if err != nil || row.Metadata[storage.SystemPromptSnapshotMetadataKey] != nil {
					t.Fatalf("snapshot leaked into session metadata: %+v %v", row, err)
				}
			}
		})
	}
}
