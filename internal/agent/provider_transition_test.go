package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/contextcompress"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/llm/jev"
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

// A session that never produced a response has nothing to summarize, so a fallback
// on its first request must not pay for a compaction round-trip.
func TestFallbackTransitionSkipsCompactionWithoutCompletedWork(t *testing.T) {
	target := &MockLLM{Response: &llm.ChatResponse{Content: "answer"}}
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
	sess.AddUserMessage("first task")
	sess.Metadata = map[string]interface{}{"provider": "fallback_chain"}
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}
	a := New(Config{Provider: "source", Model: "old", MaxSteps: 20, SystemPrompt: "system", TransitionMode: "compact_auto"}, nil, tools.NewManager(t.TempDir()), sm)
	a.llmClient = fallback.NewClient([]fallback.Node{
		{Name: "source", Model: "old", Client: &MockLLM{Err: errors.New("insufficient_quota")}},
		{Name: "target", Model: "new", Client: target},
	}, fallback.WithMaxRetries(0))
	a.config.ResolveTransitionTarget = func(*session.Session, session.ProviderEndpoint) (llm.Client, int, error) {
		return target, 65536, nil
	}

	if _, _, err := a.Run(context.Background(), sess, ""); err != nil {
		t.Fatal(err)
	}
	tr := session.CurrentProviderTransition(sess)
	if tr == nil || tr.ContextStrategy != "current" || tr.Compaction.Outcome != "skipped" {
		t.Fatalf("empty history must not compact: %+v", tr)
	}
	// One request for the real answer only; no summarization request.
	if len(target.CapturedRequests) != 1 || target.CapturedRequests[0].Messages[0].Content != "first task" {
		t.Fatalf("unexpected target requests: %+v", target.CapturedRequests)
	}
}

// Approval in confirm mode compacts by default, but an empty history still has
// nothing to summarize, so the approved switch must skip it as well.
func TestFallbackTransitionConfirmModeSkipsCompactionWithoutWork(t *testing.T) {
	target := &MockLLM{Response: &llm.ChatResponse{Content: "answer"}}
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
	sess.AddUserMessage("first task")
	sess.Metadata = map[string]interface{}{"provider": "fallback_chain"}
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}
	a := New(Config{Provider: "source", Model: "old", MaxSteps: 20, SystemPrompt: "system", TransitionMode: "confirm"}, nil, tools.NewManager(t.TempDir()), sm)
	a.config.ResolveTransitionTarget = func(*session.Session, session.ProviderEndpoint) (llm.Client, int, error) {
		return target, 65536, nil
	}
	a.llmClient = fallback.NewClient([]fallback.Node{
		{Name: "source", Model: "old", Client: &MockLLM{Err: errors.New("insufficient_quota")}},
		{Name: "target", Model: "new", Client: target},
	}, fallback.WithMaxRetries(0))

	_, _, err = a.Run(context.Background(), sess, "")
	if !errors.Is(err, ErrTransitionPaused) {
		t.Fatalf("expected pause for confirmation, got %v", err)
	}
	tr := session.CurrentProviderTransition(sess)
	if tr.ContextStrategy != "current" || tr.Compaction.Outcome != "skipped" {
		t.Fatalf("confirm mode must skip compaction on empty history: %+v", tr)
	}
}

// An assistant message that only carries runtime metadata is not completed work;
// summarizing it would be the same waste as summarizing an empty session.
func TestHasCompletedWorkIgnoresMetadataOnlyMessages(t *testing.T) {
	onlyUser := []session.Message{{Role: "user", Content: "task"}}
	if hasCompletedWork(onlyUser) {
		t.Fatal("pending user prompt alone is not completed work")
	}
	withRuntimeOnly := append(append([]session.Message(nil), onlyUser...), session.Message{
		Role:     "assistant",
		Content:  "",
		Metadata: map[string]interface{}{"llm_timing": true},
	})
	if hasCompletedWork(withRuntimeOnly) {
		t.Fatal("metadata-only assistant message is not completed work")
	}
	for name, extra := range map[string]session.Message{
		"assistant text":  {Role: "assistant", Content: "done"},
		"tool call":       {Role: "assistant", ToolCalls: []session.ToolCall{{ID: "c1", Name: "read"}}},
		"tool result":     {Role: "tool", ToolResults: []session.ToolResult{{ToolCallID: "c1"}}},
		"assistant image": {Role: "assistant", Images: []session.ImageAttachment{{URL: "https://example.com/a.png"}}},
	} {
		if !hasCompletedWork(append(append([]session.Message(nil), onlyUser...), extra)) {
			t.Fatalf("%s must count as completed work", name)
		}
	}
}

// linked_session deliberately forks a new session, so it keeps compaction.
func TestFallbackTransitionLinkedSessionKeepsCompaction(t *testing.T) {
	target := &MockLLM{Responses: []*llm.ChatResponse{{Content: "summary"}, {Content: "answer"}}}
	a, sess, _ := transitionAgent(t, "linked_session", target)
	a.llmClient = fallback.NewClient([]fallback.Node{
		{Name: "source", Model: "old", Client: &MockLLM{Err: errors.New("insufficient_quota")}},
		{Name: "target", Model: "new", Client: target},
	}, fallback.WithMaxRetries(0))

	_, _, err := a.Run(context.Background(), sess, "")
	if !errors.Is(err, ErrTransitionPaused) {
		t.Fatalf("expected pause for linked continuation, got %v", err)
	}
	tr := session.CurrentProviderTransition(sess)
	if tr.ContextStrategy != "compact" {
		t.Fatalf("linked_session must keep compact strategy, got %q", tr.ContextStrategy)
	}
}

// With real work in the transcript compaction stays the default strategy.
func TestFallbackTransitionCompactsWhenWorkExists(t *testing.T) {
	target := &MockLLM{Responses: []*llm.ChatResponse{{Content: "summary"}, {Content: "answer"}}}
	a, sess, sm := transitionAgent(t, "compact_auto", target)
	delete(sess.Metadata, session.TransitionMetadataKey)
	_ = sm.Save(sess)
	a.llmClient = fallback.NewClient([]fallback.Node{
		{Name: "source", Model: "old", Client: &MockLLM{Err: errors.New("503 unavailable")}},
		{Name: "target", Model: "new", Client: target},
	}, fallback.WithMaxRetries(0))

	if _, _, err := a.Run(context.Background(), sess, ""); err != nil {
		t.Fatal(err)
	}
	tr := session.CurrentProviderTransition(sess)
	if tr == nil || tr.ContextStrategy != "compact" || tr.Compaction.Outcome != "succeeded" {
		t.Fatalf("history with work must compact: %+v", tr)
	}
}

// fakeRelevanceJev scores a candidate 4 when it contains "RELEVANT", else 0.
func fakeRelevanceJev(t *testing.T, status int) *jev.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		var req jev.SystemOneRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		state := req.StateString()
		answers := map[string]jev.Answer{}
		for key := range req.Questions {
			body := state[strings.Index(state, key+":\n"):]
			if next := strings.Index(body[1:], "\nitem_"); next >= 0 {
				body = body[:next+1]
			}
			score := 0.0
			if strings.Contains(body, "RELEVANT") {
				score = 4
			}
			answers[key] = jev.Answer{Type: "score", Score: &score, Confidence: 0.95}
		}
		_ = json.NewEncoder(w).Encode(jev.SystemOneResponse{Answers: answers})
	}))
	t.Cleanup(server.Close)
	return jev.NewClient("key", "", server.URL+"/v1")
}

func addJevHistory(sess *session.Session) {
	sess.AddAssistantMessage("", []session.ToolCall{{ID: "c1", Name: "bash", Input: json.RawMessage(`{}`)}, {ID: "c2", Name: "read", Input: json.RawMessage(`{}`)}})
	sess.AddToolResult([]session.ToolResult{
		{ToolCallID: "c1", Name: "bash", Content: "RELEVANT\n" + strings.Repeat("kept line\n", 300)},
		{ToolCallID: "c2", Name: "read", Content: strings.Repeat("noise line\n", 300)},
	})
}

func TestProviderTransitionJevFiltersHistory(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			target := &MockLLM{Responses: []*llm.ChatResponse{{Content: "summary"}, {Content: "answer"}}, Response: &llm.ChatResponse{Content: "answer"}}
			a, sess, _ := transitionAgent(t, "compact_auto", target)
			pending := sess.Messages[len(sess.Messages)-1]
			sess.Messages = sess.Messages[:len(sess.Messages)-1]
			addJevHistory(sess)
			sess.AddMessage(pending)
			a.config.RelevanceGate = &contextcompress.RelevanceGate{Client: fakeRelevanceJev(t, status)}
			if _, _, err := a.Run(context.Background(), sess, "ignored"); err != nil {
				t.Fatal(err)
			}
			c := session.CurrentProviderTransition(sess).Compaction
			chunk := target.CapturedRequests[0].Messages[0].Content
			if status != http.StatusOK {
				// Fail-open: no Jev badge, full history summarized.
				if c.JevUsed || !strings.Contains(chunk, "noise line") {
					t.Fatalf("jev failure must keep history: %+v", c)
				}
				return
			}
			if !c.JevUsed || c.JevScored != 2 || c.JevDropped != 1 || c.JevDroppedBytes != 3300 {
				t.Fatalf("jev stats %+v", c)
			}
			if !strings.Contains(chunk, "RELEVANT") || strings.Contains(chunk, "noise line") || !strings.Contains(chunk, "omitted before summarization") {
				t.Fatalf("summarizer input not filtered: %.200s", chunk)
			}
			if c.BeforeEstimate < 6000 {
				t.Fatalf("before must measure unfiltered history: %d", c.BeforeEstimate)
			}
			// The archived transcript keeps the original output.
			for _, m := range sess.Messages {
				for _, tr := range m.ToolResults {
					if tr.ToolCallID == "c2" && !strings.Contains(tr.Content, "noise line") {
						t.Fatal("archived tool output was mutated")
					}
				}
			}
		})
	}
}

func TestContextCompactionJevFiltersHistory(t *testing.T) {
	t.Setenv(envCompactionTriggerPercent, "50")
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
	sess.AddUserMessage("fix the bug")
	addJevHistory(sess)
	sess.AddAssistantMessage("checked", nil)
	// Two more turns push the tool batch out of the retained tail into the summarized part.
	sess.AddUserMessage("next step")
	sess.AddAssistantMessage("done", nil)
	sess.AddUserMessage("continue")
	metadataSetFloat(sess, metadataCurrentContextTokens, 600)
	client := &MockLLM{Response: &llm.ChatResponse{Content: "summary"}}
	ag := New(Config{ContextWindow: 1000, RelevanceGate: &contextcompress.RelevanceGate{Client: fakeRelevanceJev(t, http.StatusOK)}}, client, nil, sm)
	if _, compacted, err := ag.maybeCompactContext(context.Background(), sess, 1); !compacted || err != nil {
		t.Fatalf("compacted=%v err=%v", compacted, err)
	}
	var summary *session.Message
	for i := range sess.Messages {
		if sess.Messages[i].Metadata[messageMetadataCompaction] == true {
			summary = &sess.Messages[i]
		}
	}
	if summary == nil || summary.Metadata["jev_used"] != true || summary.Metadata["jev_dropped"] != 1 || summary.Metadata["jev_scored"] != 2 {
		t.Fatalf("summary metadata: %+v", summary)
	}
	raw, _ := json.Marshal(client.CapturedRequests[0].Messages)
	if strings.Contains(string(raw), "noise line") || !strings.Contains(string(raw), "RELEVANT") {
		t.Fatal("compaction request not filtered")
	}
}

func TestCompactionRelevanceTaskSkipsSyntheticPrompts(t *testing.T) {
	sess := &session.Session{}
	sess.AddUserMessage("fix the parser")
	sess.AddMessage(session.Message{Role: "user", Content: "Continue with the task based on the summary above.", Metadata: map[string]interface{}{"synthetic_continuation": true}})
	sess.AddUserMessage(transitionContinuationPrompt)
	if task := compactionRelevanceTask(sess); !strings.Contains(task, "fix the parser") || strings.Contains(task, "Continue") {
		t.Fatalf("task = %q", task)
	}
}
