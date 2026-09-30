package http

import (
	"context"
	"encoding/json"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDockerDelegationCancellationStopsChildSession(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{}, 1)
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/sessions":
			_, _ = w.Write([]byte(`{"id":"child-cancel"}`))
		case "/sessions/child-cancel/chat/stream":
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			cancel()
			<-r.Context().Done()
		case "/sessions/child-cancel/cancel":
			stopped <- struct{}{}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected URL %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer child.Close()
	result, err := s.runDockerAgentDelegation(ctx, &LocalDockerAgent{ID: "container-1", Name: "reviewer", Running: true, HostPort: 12345, APIURL: child.URL}, "review")
	if err != nil || result == nil || result.Success {
		t.Fatalf("expected canceled delegation, got %+v %v", result, err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("child session was not canceled")
	}
}

func TestMCPBridgeDelegationErrorPreservesChildMetadata(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	sess, _ := s.sessionManager.Create("build")
	s.toolManager.Register(&bridgeDelegationProbe{name: "delegate_to_agent", execute: func(context.Context, json.RawMessage) (*tools.Result, error) {
		return &tools.Result{Success: false, Error: "child failed", Metadata: map[string]interface{}{"child_session_id": "child-failed", "agent_name": "reviewer"}}, nil
	}})
	text, failed, rpcErr := s.mcpBridgeCallTool(context.Background(), sess, json.RawMessage(`{"name":"delegate_to_agent"}`))
	var payload map[string]interface{}
	if !failed || rpcErr != nil || json.Unmarshal([]byte(text), &payload) != nil || payload["child_session_id"] != "child-failed" || payload["error"] != "child failed" {
		t.Fatalf("lost child metadata: %s", text)
	}
}

func TestDelegatedRunCancellationBeforeRegistration(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest() // The client disconnected before the handler could register a run.
	for _, delegated := range []bool{false, true} {
		sess := &session.Session{}
		if delegated {
			sess.Metadata = map[string]interface{}{"source": "delegate_to_agent"}
		}
		ctx, cancel := s.chatStreamRunContext(requestCtx, sess)
		if delegated {
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("delegated run survived disconnect before registration")
			}
		} else if ctx.Err() != nil {
			t.Fatal("ordinary chat must survive disconnect")
		}
		cancel()
	}
}
