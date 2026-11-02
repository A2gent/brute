package http

import (
	"context"
	"encoding/json"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"
)

// fakeDirectChild serves a direct child that is running until it is canceled,
// then reports paused with the partial turn it saved.
func fakeDirectChild(t *testing.T, parentID string, messages []MessageResponse) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	stopped := &atomic.Bool{}
	child := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.Method == stdhttp.MethodPost && r.URL.Path == "/sessions/child/cancel" {
			stopped.Store(true)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		status := "running"
		if stopped.Load() {
			status = "paused"
		}
		_ = json.NewEncoder(w).Encode(SessionResponse{ID: "child", Status: status, Messages: messages, Metadata: map[string]interface{}{"source": "direct_agent_session", "parent_session_id": parentID}})
	}))
	t.Cleanup(child.Close)
	return child, stopped
}

func partialChildTurn() []MessageResponse {
	return []MessageResponse{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "old answer", Metadata: runtimeTurn("old", nil)},
		{Role: "user", Content: "plan the work"},
		{Role: "assistant", Content: "Looking at tasks", Metadata: runtimeTurn("now", map[string]interface{}{"runtime_tools": map[string]interface{}{"t1": map[string]interface{}{"name": "tasks"}}, "llm_usage": 1})},
	}
}

func TestCanceledDirectAgentRunKeepsChildActivity(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.Metadata = map[string]interface{}{"docker_agent_id": "direct"}
	sess.AddUserMessage("plan the work")
	child, stopped := fakeDirectChild(t, sess.ID, partialChildTurn())
	addDockerChildSessionEntry(sess, &LocalDockerAgent{APIURL: child.URL}, "child", nil)

	_ = server.finalizeSessionRunWithoutStreaming(context.Background(), sess, sessionRunResult{DirectAgent: true}, context.Canceled)

	if !stopped.Load() {
		t.Fatal("stopped parent did not cancel the child")
	}
	loaded, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := server.messagesToResponse(loaded.Messages)
	if loaded.Status != session.StatusPaused || len(got) != 2 {
		t.Fatalf("status=%s messages=%+v", loaded.Status, got)
	}
	step := got[1]
	if step.Content != "Looking at tasks" || step.Metadata["runtime_tools"] == nil || step.Metadata["llm_usage"] != nil || step.Metadata[directAgentActivityMarker] != true {
		t.Fatalf("copied step = %+v", step)
	}
}

func TestInterruptedDirectAgentRunSkipsAlreadyCopiedTurn(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess := &session.Session{ID: "parent", Metadata: map[string]interface{}{"docker_agent_id": "direct"}}
	sess.AddUserMessage("plan the work")
	sess.AddAssistantMessageWithMetadata("Looking at tasks", nil, map[string]interface{}{"runtime_turn_id": "now", directAgentActivityMarker: true})
	sess.AddUserMessage("next request")
	child, _ := fakeDirectChild(t, sess.ID, partialChildTurn())
	addDockerChildSessionEntry(sess, &LocalDockerAgent{APIURL: child.URL}, "child", nil)

	server.persistInterruptedDirectAgentTurn(sess, ChatResponse{}, true)

	if len(sess.Messages) != 3 {
		t.Fatalf("previous turn copied again: %+v", sess.Messages)
	}
}

func TestHydrateDirectAgentShowsPausedTurn(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	parent := &session.Session{ID: "parent", Status: session.StatusPaused, Metadata: map[string]interface{}{"docker_agent_id": "direct"}}
	child, stopped := fakeDirectChild(t, parent.ID, partialChildTurn())
	stopped.Store(true)
	addDockerChildSessionEntry(parent, &LocalDockerAgent{APIURL: child.URL}, "child", nil)
	stored := []MessageResponse{{Role: "user", Content: "plan the work"}}

	got := server.hydrateDirectAgentActivity(context.Background(), parent, stored, true)

	if len(got) != 2 || got[1].Content != "Looking at tasks" {
		t.Fatalf("paused turn activity missing: %+v", got)
	}
}

func TestInterruptedDirectAgentRunStopsChildAfterFailedLookup(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess := &session.Session{ID: "parent", Metadata: map[string]interface{}{"docker_agent_id": "direct"}}
	sess.AddUserMessage("plan the work")
	inner, stopped := fakeDirectChild(t, sess.ID, partialChildTurn())
	gets := &atomic.Int32{}
	child := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.Method == stdhttp.MethodGet && gets.Add(1) == 1 {
			w.WriteHeader(stdhttp.StatusServiceUnavailable)
			return
		}
		proxyReq, _ := stdhttp.NewRequest(r.Method, inner.URL+r.URL.RequestURI(), r.Body)
		resp, err := stdhttp.DefaultClient.Do(proxyReq)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer child.Close()
	addDockerChildSessionEntry(sess, &LocalDockerAgent{APIURL: child.URL}, "child", nil)

	server.persistInterruptedDirectAgentTurn(sess, ChatResponse{}, true)

	if !stopped.Load() || len(sess.Messages) != 2 {
		t.Fatalf("stopped=%v messages=%+v", stopped.Load(), sess.Messages)
	}
}

// Stop must not overwrite the partial turn the canceled run saves after it.
func TestCancelSessionKeepsPartialTurnSavedByCanceledRun(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("plan the work")
	sess.SetStatus(session.StatusRunning)
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	server.registerActiveSessionRun(sess.ID, func() {
		go func() {
			time.Sleep(150 * time.Millisecond)
			sess.AddAssistantMessage("partial work", nil)
			sess.SetStatus(session.StatusPaused)
			_ = server.sessionManager.Save(sess)
		}()
	})

	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(stdhttp.MethodPost, "/sessions/"+sess.ID+"/cancel", nil))

	loaded, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != stdhttp.StatusOK || loaded.Status != session.StatusPaused || len(loaded.Messages) != 2 {
		t.Fatalf("code=%d status=%s messages=%+v", rec.Code, loaded.Status, loaded.Messages)
	}
}
