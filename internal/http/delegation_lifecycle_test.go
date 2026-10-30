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

func delegationJobTestContext(t *testing.T, s *Server) (context.Context, string) {
	t.Helper()
	sess, err := s.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	return context.WithValue(context.Background(), "session_id", sess.ID), sess.ID
}

func decodeJobOutput(t *testing.T, result *tools.Result) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if result == nil || json.Unmarshal([]byte(result.Output), &payload) != nil {
		t.Fatalf("unexpected job output: %+v", result)
	}
	return payload
}

func TestDelegationJobLifecycleSurvivesAbortedMCPCall(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	parentCtx, _ := delegationJobTestContext(t, s)
	callCtx, abortCall := context.WithCancel(parentCtx)
	release := make(chan struct{})
	runCtx := make(chan context.Context, 1)
	result, err := s.startDelegationJob(callCtx, "reviewer", func(ctx context.Context) (*tools.Result, error) {
		runCtx <- ctx
		tools.ReportProgress(ctx, tools.ProgressEvent{Content: "child created", Metadata: map[string]interface{}{"child_session_id": "child-1"}})
		tools.ReportProgress(ctx, tools.ProgressEvent{Content: "running bash"})
		<-release
		return &tools.Result{Success: true, Output: `{"response":"done"}`}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	started := decodeJobOutput(t, result)
	jobID, _ := started["job_id"].(string)
	if started["status"] != "running" || started["child_session_id"] != "child-1" || jobID == "" {
		t.Fatalf("unexpected start payload: %v", started)
	}
	abortCall()
	if (<-runCtx).Err() != nil {
		t.Fatal("aborting the MCP call must not cancel the child run")
	}

	wait := &waitForAgentTool{server: s}
	pending, _ := wait.Execute(parentCtx, json.RawMessage(`{"job_id":"`+jobID+`","max_wait_seconds":1}`))
	progress := decodeJobOutput(t, pending)
	if progress["status"] != "running" || len(progress["recent_progress"].([]interface{})) != 2 {
		t.Fatalf("expected running status with progress, got %v", progress)
	}

	close(release)
	final, _ := wait.Execute(parentCtx, json.RawMessage(`{"job_id":"`+jobID+`","max_wait_seconds":5}`))
	if final == nil || !final.Success || final.Output != `{"response":"done"}` || final.Metadata["job_id"] != jobID {
		t.Fatalf("unexpected final result: %+v", final)
	}
	if gone, _ := wait.Execute(parentCtx, json.RawMessage(`{"job_id":"`+jobID+`"}`)); gone.Success {
		t.Fatal("delivered job must be removed")
	}
}

func TestDelegationJobFastFailureReturnsDirectly(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	ctx, _ := delegationJobTestContext(t, s)
	result, _ := s.startDelegationJob(ctx, "missing", func(context.Context) (*tools.Result, error) {
		return &tools.Result{Success: false, Error: "agent not found"}, nil
	})
	if result == nil || result.Success || result.Error != "agent not found" {
		t.Fatalf("expected direct failure, got %+v", result)
	}
}

func TestDelegationJobCancelAndSessionScope(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	ctx, _ := delegationJobTestContext(t, s)
	otherCtx, _ := delegationJobTestContext(t, s)
	result, _ := s.startDelegationJob(ctx, "reviewer", func(ctx context.Context) (*tools.Result, error) {
		tools.ReportProgress(ctx, tools.ProgressEvent{Metadata: map[string]interface{}{"child_session_id": "child-2"}})
		<-ctx.Done()
		return &tools.Result{Success: false, Error: ctx.Err().Error()}, nil
	})
	jobID, _ := decodeJobOutput(t, result)["job_id"].(string)
	args := json.RawMessage(`{"job_id":"` + jobID + `"}`)
	if foreign, _ := (&waitForAgentTool{server: s}).Execute(otherCtx, args); foreign.Success {
		t.Fatal("another session must not see the job")
	}
	canceled, _ := (&cancelAgentTool{server: s}).Execute(ctx, args)
	if decodeJobOutput(t, canceled)["status"] != "canceled" {
		t.Fatalf("unexpected cancel result: %+v", canceled)
	}
}

func TestMCPBridgeDelegationRunsAsync(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	sess, _ := s.sessionManager.Create("build")
	async := make(chan bool, 1)
	s.toolManager.Register(&bridgeDelegationProbe{name: "delegate_to_agent", execute: func(ctx context.Context, _ json.RawMessage) (*tools.Result, error) {
		async <- delegationAsync(ctx)
		return &tools.Result{Success: true, Output: "ok"}, nil
	}})
	if _, failed, rpcErr := s.mcpBridgeCallTool(context.Background(), sess, json.RawMessage(`{"name":"delegate_to_agent"}`)); failed || rpcErr != nil {
		t.Fatal("bridge call failed")
	}
	if !<-async {
		t.Fatal("bridge delegation must run in async mode")
	}
	if delegationAsync(context.Background()) {
		t.Fatal("native calls must stay synchronous")
	}
}

func TestParentAgentToolsForwardDelegationAsync(t *testing.T) {
	parent, _, child, childSess, ctx, _ := parentAgentToolsTestPair(t)
	async := make(chan bool, 1)
	parent.toolManager.Register(&bridgeDelegationProbe{name: "delegate_to_agent", execute: func(ctx context.Context, _ json.RawMessage) (*tools.Result, error) {
		async <- delegationAsync(ctx)
		return &tools.Result{Success: true, Output: "ok"}, nil
	}})
	manager := child.toolManagerForSession(childSess)
	for _, want := range []bool{false, true} {
		callCtx := ctx
		if want {
			callCtx = withDelegationAsync(ctx)
		}
		if _, err := manager.Execute(callCtx, "delegate_to_agent", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if got := <-async; got != want {
			t.Fatalf("parent async = %v, want %v", got, want)
		}
	}
}
