package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func transitionAgent(t *testing.T, mode string, target *MockLLM) (*Agent, *session.Session, *session.Manager) {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	sm := session.NewManager(store)
	sess, err := sm.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("original task")
	sess.AddAssistantMessage("work already done", nil)
	sess.AddUserMessage("pending prompt")
	sess.Metadata = map[string]interface{}{"provider": "source", "model": "old", "last_response_id": "old-response", "provider_session_cursor": "old-cursor"}
	tr, _ := session.NewProviderTransition("transition-1", session.ProviderEndpoint{Provider: "source", Model: "old"}, session.ProviderEndpoint{Provider: "target", Model: "new"}, "manual", mode)
	session.PutProviderTransition(sess, tr)
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}
	a := New(Config{Provider: "source", Model: "old", MaxSteps: 20, SystemPrompt: "system", TransitionMode: mode,
		ResolveTransitionTarget: func(s *session.Session, e session.ProviderEndpoint) (llm.Client, int, error) {
			return target, 65536, nil
		}}, &MockLLM{Err: errors.New("source must not run")}, tools.NewManager(t.TempDir()), sm)
	return a, sess, sm
}
func TestProviderTransitionModes(t *testing.T) {
	for _, mode := range []string{"compact_auto", "switch_auto", "confirm", "linked_session"} {
		t.Run(mode, func(t *testing.T) {
			target := &MockLLM{Responses: []*llm.ChatResponse{{Content: "summary"}, {Content: "answer"}}, Response: &llm.ChatResponse{Content: "answer"}}
			a, sess, sm := transitionAgent(t, mode, target)
			_, _, err := a.Run(context.Background(), sess, "ignored")
			tr := session.CurrentProviderTransition(sess)
			if mode == "confirm" {
				if !errors.Is(err, ErrTransitionPaused) || len(target.CapturedRequests) != 0 || tr.Phase != "awaiting_confirmation" {
					t.Fatalf("consent: %v %+v", err, tr)
				}
				return
			}
			if mode == "linked_session" {
				if !errors.Is(err, ErrTransitionPaused) || tr.Phase != "linked" {
					t.Fatalf("linked: %v %+v", err, tr)
				}
				linked, err := sm.Get(tr.LinkedSessionID)
				if err != nil || linked.ParentID != nil || linked.Metadata["continuation_source_id"] != sess.ID {
					t.Fatalf("not a real continuation: %+v %v", linked, err)
				}
				return
			}
			if err != nil || tr.Phase != "completed" {
				t.Fatalf("%v %+v", err, tr)
			}
			expected := 2
			if mode == "switch_auto" {
				expected = 1
			}
			if mode == "compact_auto" && (tr.Compaction.StartedAt == nil || tr.Compaction.DurationMs < 0) {
				t.Fatalf("compaction timing missing %+v", tr.Compaction)
			}
			if len(target.CapturedRequests) != expected {
				t.Fatalf("calls %d", len(target.CapturedRequests))
			}
			req := target.CapturedRequests[expected-1]
			if req.Model != "new" || req.PreviousResponseID != "" || req.ProviderSessionCursor != "" {
				t.Fatalf("stale request %+v", req)
			}
			found := false
			for _, m := range req.Messages {
				if m.Content == "pending prompt" {
					found = true
				}
			}
			if !found || sess.Messages[0].Content != "original task" {
				t.Fatal("pending or archive lost")
			}
		})
	}
}
func TestProviderTransitionRollbackAndRestart(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "empty"}[empty], func(t *testing.T) {
			target := &MockLLM{Response: &llm.ChatResponse{Content: " "}}
			if !empty {
				target.Err = errors.New("503")
			}
			a, sess, sm := transitionAgent(t, "compact_auto", target)
			original := append([]session.Message(nil), sess.Messages...)
			_, _, err := a.Run(context.Background(), sess, "")
			if !errors.Is(err, ErrTransitionPaused) || !reflect.DeepEqual(original, sess.Messages) || sess.Metadata["provider"] != "source" {
				t.Fatalf("rollback %v", err)
			}
			fresh, _ := sm.Get(sess.ID)
			tr := session.CurrentProviderTransition(fresh)
			if tr.Phase != "paused" || tr.Compaction.Outcome != "failed" {
				t.Fatalf("restart %+v", tr)
			}
		})
	}
}
func TestProviderTransitionApprovalAfterReload(t *testing.T) {
	target := &MockLLM{Responses: []*llm.ChatResponse{{Content: "summary"}, {Content: "answer"}}}
	a, sess, sm := transitionAgent(t, "confirm", target)
	_, _, _ = a.Run(context.Background(), sess, "")
	sess, _ = sm.Get(sess.ID)
	tr := session.CurrentProviderTransition(sess)
	changed, err := tr.Decide("approve")
	if err != nil || !changed {
		t.Fatal(err)
	}
	session.PutProviderTransition(sess, tr)
	_ = sm.Save(sess)
	if changed, err = tr.Decide("approve"); changed || err != nil {
		t.Fatal("approval not idempotent")
	}
	if _, err = tr.Decide("reject"); err == nil {
		t.Fatal("conflicting decision accepted")
	}
	_, _, err = a.Run(context.Background(), sess, "")
	if err != nil || len(target.CapturedRequests) != 2 {
		t.Fatalf("resume %v", err)
	}
}
func TestProviderTransitionCancellation(t *testing.T) {
	target := &MockLLM{Response: &llm.ChatResponse{Content: "summary"}}
	a, sess, _ := transitionAgent(t, "compact_auto", target)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _ = a.Run(ctx, sess, "")
	if len(target.CapturedRequests) != 0 {
		t.Fatal("canceled request disclosed context")
	}
}
func TestProviderFallbackRebuildsRequest(t *testing.T) {
	target := &MockLLM{Responses: []*llm.ChatResponse{{Content: "summary"}, {Content: "answer"}}}
	a, sess, sm := transitionAgent(t, "compact_auto", target)
	delete(sess.Metadata, session.TransitionMetadataKey)
	_ = sm.Save(sess)
	a.llmClient = fallback.NewClient([]fallback.Node{{Name: "source", Model: "old", Client: &MockLLM{Err: errors.New("503 unavailable")}}, {Name: "target", Model: "new", Client: target}}, fallback.WithMaxRetries(0))
	_, _, err := a.Run(context.Background(), sess, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(target.CapturedRequests) != 2 || target.CapturedRequests[1].Messages[0].Content != "summary" {
		t.Fatalf("not rebuilt: %+v", target.CapturedRequests)
	}
}
