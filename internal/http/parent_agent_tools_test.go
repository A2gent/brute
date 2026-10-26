package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func parentAgentToolsTestPair(t *testing.T) (*Server, *session.Session, *Server, *session.Session, context.Context, func()) {
	t.Helper()
	parent := newMCPBridgeTestServer(t)
	parentSess, _ := parent.sessionManager.Create("build")
	parentHTTP := httptest.NewServer(parent.router)
	t.Cleanup(parentHTTP.Close)
	child := newMCPBridgeTestServer(t)
	child.parentAgentToolsURL = trustedParentAgentToolsURL(parentHTTP.URL)
	childSess, _ := child.sessionManager.Create("build")
	childSess.Metadata = map[string]interface{}{"source": "direct_agent_session", "parent_session_id": parentSess.ID}
	if err := child.sessionManager.Save(childSess); err != nil {
		t.Fatal(err)
	}
	headers, revoke, err := parent.directAgentToolsHeaders(t.Context(), parentSess, agentdef.RuntimeHost)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revoke)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header = headers
	remove, err := child.acquireParentAgentTools(t.Context(), req, childSess)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	ctx := context.WithValue(t.Context(), "session_id", childSess.ID)
	return parent, parentSess, child, childSess, ctx, remove
}

func TestParentAgentToolsIsolatedCatalogAndDispatch(t *testing.T) {
	parent, parentSess, child, childSess, ctx, remove := parentAgentToolsTestPair(t)
	projectID, otherID := "parent-project", "other-project"
	now := time.Now()
	for _, id := range []string{projectID, otherID} {
		if err := parent.store.SaveProject(&storage.Project{ID: id, Name: id, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	parentSess.ProjectID = &projectID
	if err := parent.sessionManager.Save(parentSess); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id      string
		project *string
	}{{"global-reviewer", nil}, {"project-reviewer", &projectID}, {"other-reviewer", &otherID}} {
		definition := fmt.Sprintf("version: \"1\"\nagent:\n  id: %s\n  name: %s\nruntime:\n  type: host\n", item.id, item.id)
		if err := parent.store.SaveAgentDefinition(&storage.AgentDefinitionRecord{ID: item.id, Name: item.id, ProjectID: item.project, Runtime: "host", DefinitionYAML: definition, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	local, err := child.toolManager.Execute(ctx, "list_agents", json.RawMessage(`{}`))
	if err != nil || strings.Contains(local.Output, "reviewer") {
		t.Fatalf("child should have isolated catalog: %+v %v", local, err)
	}
	manager := child.toolManagerForSession(childSess)
	result, err := manager.Execute(ctx, "list_agents", json.RawMessage(`{}`))
	if err != nil || !result.Success || !strings.Contains(result.Output, "global-reviewer") || !strings.Contains(result.Output, "project-reviewer") || strings.Contains(result.Output, "other-reviewer") {
		t.Fatalf("parent catalog scope: %+v %v", result, err)
	}
	for _, name := range []string{"delegate_to_agent", "delegate_to_subagent"} {
		parent.toolManager.Register(&bridgeDelegationProbe{name: name, execute: func(callCtx context.Context, args json.RawMessage) (*tools.Result, error) {
			if callCtx.Value("session_id") != parentSess.ID || callCtx.Value("projectID") != projectID {
				t.Errorf("wrong parent scope: %v %v", callCtx.Value("session_id"), callCtx.Value("projectID"))
			}
			return &tools.Result{Success: true, Output: "parent-delegated"}, nil
		}})
		result, err = manager.Execute(ctx, name, json.RawMessage(`{"agent_id":"global-reviewer","task":"review"}`))
		if err != nil || !result.Success || result.Output != "parent-delegated" {
			t.Fatalf("dispatch %s: %+v %v", name, result, err)
		}
	}
	for _, name := range []string{"parallel", "pipeline"} {
		result, err = manager.Execute(ctx, name, json.RawMessage(`{"steps":[{"tool":"delegate_to_agent","args":{"agent_id":"global-reviewer","task":"review"}}]}`))
		if err != nil || !result.Success || !strings.Contains(result.Output, "parent-delegated") {
			t.Fatalf("%s did not see wrapper: %+v %v", name, result, err)
		}
	}
	// A CLI's local MCP bridge builds a fresh manager and must see the same wrapper.
	text, failed, rpcErr := child.mcpBridgeCallTool(ctx, childSess, json.RawMessage(`{"name":"list_agents","arguments":{}}`))
	if failed || rpcErr != nil || !strings.Contains(text, "project-reviewer") {
		t.Fatalf("CLI bridge did not see parent catalog: %s %v %v", text, failed, rpcErr)
	}
	if _, ok := child.toolManager.Get("read"); !ok {
		t.Fatal("local nondelegation tools lost")
	}
	if tool, _ := child.toolManager.Get("delegate_to_agent"); tool == nil {
		t.Fatal("shared manager mutated")
	} else if _, wrapped := tool.(*parentAgentTool); wrapped {
		t.Fatal("shared manager wrapped")
	}
	remove()
	if _, err := manager.Execute(ctx, "delegate_to_agent", json.RawMessage(`{}`)); err == nil {
		t.Fatal("stale manager retained capability after turn")
	}
}

func TestParentAgentToolsDisabledPolicies(t *testing.T) {
	parent, _, child, childSess, ctx, _ := parentAgentToolsTestPair(t)
	settings, _ := parent.store.GetSettings()
	settings[disabledToolsSettingKey] = `["delegate_to_agent","delegate_to_subagent","list_agents"]`
	if err := parent.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"list_agents", "delegate_to_agent", "delegate_to_subagent"} {
		result, err := child.toolManagerForSession(childSess).Execute(ctx, name, json.RawMessage(`{}`))
		if err != nil || result.Success || !strings.Contains(result.Error, "tool not found") {
			t.Fatalf("parent disabled %s: %+v %v", name, result, err)
		}
	}
	settings, _ = child.store.GetSettings()
	settings[disabledToolsSettingKey] = `["delegate_to_agent","delegate_to_subagent","list_agents"]`
	if err := child.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	manager := child.toolManagerForSession(childSess)
	for _, name := range []string{"list_agents", "delegate_to_agent", "delegate_to_subagent"} {
		if _, ok := manager.Get(name); ok {
			t.Fatalf("child disabled %s was re-enabled", name)
		}
	}
	for _, name := range []string{"parallel", "pipeline"} {
		result, err := manager.Execute(ctx, name, json.RawMessage(`{"steps":[{"tool":"delegate_to_agent","args":{}}]}`))
		if err == nil && strings.Contains(result.Output, "parent-delegated") {
			t.Fatal("compound bypassed disabled tool")
		}
	}
}

func TestParentAgentToolsCapabilityScopeAndRevocation(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	sess, _ := s.sessionManager.Create("build")
	ctx, cancel := context.WithCancel(t.Context())
	headers, revoke, err := s.directAgentToolsHeaders(ctx, sess, agentdef.RuntimeHost)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	token := headers.Get(parentAgentToolsHeader)
	_, tokenCtx, ok := s.mcpBridge.resolve(token)
	if !ok {
		t.Fatal("token not minted")
	}
	// The existing relay marker must not widen the new capability.
	tokenCtx = context.WithValue(tokenCtx, mcpRelayChildKey{}, true)
	for _, name := range []string{"list_agents", "delegate_to_agent", "delegate_to_subagent", "read", "question", "delegate_to_external_agent", "functions.delegate_to_agent", "parallel", "pipeline", "mcp_call", "arbitrary"} {
		if mcpBridgeToolExposedContext(tokenCtx, name) != parentAgentToolAllowed(name) {
			t.Fatalf("scope mismatch: %s", name)
		}
		if parentAgentToolAllowed(name) {
			continue
		}
		code, rpc := serveMCPBridge(t, s, newMCPBridgeRequest(t, sess.ID, token, mcpBridgeRPC(t, 1, "tools/call", map[string]interface{}{"name": name})))
		_, failed := mcpBridgeCallResultText(t, rpc)
		if code != http.StatusOK || !failed {
			t.Fatalf("capability allowed %s: %d %+v", name, code, rpc)
		}
	}
	for _, def := range s.mcpBridgeToolListContext(tokenCtx, sess) {
		if !parentAgentToolAllowed(def["name"].(string)) {
			t.Fatalf("scope list leak: %+v", def)
		}
	}
	other, _ := s.sessionManager.Create("build")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, newMCPBridgeRequest(t, other.ID, token, mcpBridgeRPC(t, 1, "tools/list", nil)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("session binding: %d", rec.Code)
	}
	cancel()
	if _, _, ok := s.mcpBridge.resolve(token); ok {
		t.Fatal("token usable after cancellation")
	}
	revoke()
	s.mcpBridge.mu.Lock()
	_, exists := s.mcpBridge.tokens[token]
	s.mcpBridge.mu.Unlock()
	if exists {
		t.Fatal("token not removed after completion")
	}
	encoded, _ := json.Marshal(sess)
	if strings.Contains(string(encoded), token) {
		t.Fatal("token persisted in session")
	}
}

func TestParentAgentToolsRejectDelegatedAndUntrusted(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	s.parentAgentToolsURL = "http://127.0.0.1:1234"
	sess, _ := s.sessionManager.Create("build")
	for _, runtime := range []string{agentdef.RuntimeDocker, agentdef.RuntimeHost} {
		sess.Metadata = map[string]interface{}{"source": "delegate_to_agent"}
		headers, revoke, err := s.directAgentToolsHeaders(t.Context(), sess, runtime)
		if err != nil || len(headers) != 0 {
			t.Fatalf("delegated token minted: %v %v", headers, err)
		}
		revoke()
	}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set(parentAgentToolsHeader, "secret")
	for _, metadata := range []map[string]interface{}{
		{"source": "delegate_to_agent", "parent_session_id": "parent"},
		{"source": "direct_agent_session", "parent_session_id": "parent", "sub_agent_id": "delegated"},
		{"source": "direct_agent_session"},
	} {
		sess.Metadata = metadata
		if _, err := s.acquireParentAgentTools(t.Context(), req, sess); err == nil {
			t.Fatalf("accepted invalid source: %v", metadata)
		}
	}
	sess.Metadata = map[string]interface{}{"source": "direct_agent_session", "parent_session_id": "parent"}
	remove, err := s.acquireParentAgentTools(t.Context(), req, sess)
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	sess.Metadata["sub_agent_id"] = "now-delegated"
	if s.parentAgentToolsForSession(sess) != nil {
		t.Fatal("delegated source used existing capability")
	}
	for _, raw := range []string{"http://example.com:1234", "http://localhost:1234", "https://127.0.0.1:1234", "http://secret@127.0.0.1:1234", "http://127.0.0.1:1234/?token=secret", "http://127.0.0.1:1234/evil", "http://127.0.0.1:1234/#secret", "http://127.0.0.1"} {
		if trustedParentAgentToolsURL(raw) != "" {
			t.Fatalf("trusted invalid parent URL %s", raw)
		}
	}
}

func TestParentAgentToolsStreamHeaderNotPersisted(t *testing.T) {
	parent := newMCPBridgeTestServer(t)
	sess, _ := parent.sessionManager.Create("build")
	headers, revoke, err := parent.directAgentToolsHeaders(t.Context(), sess, agentdef.RuntimeHost)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	token := headers.Get(parentAgentToolsHeader)
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get(parentAgentToolsHeader) != token || strings.Contains(string(body), token) {
			t.Error("capability must be header only")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, "{\"type\":\"done\",\"content\":\"ok\",\"status\":\"completed\"}\n")
	}))
	defer child.Close()
	_, err = postLocalDockerAgentChatStreamWithHeaders(t.Context(), child.Client(), child.URL, ChatRequest{Message: "work"}, headers, nil)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := parent.sessionManager.Get(sess.ID)
	encoded, _ := json.Marshal(persisted)
	if strings.Contains(string(encoded), token) {
		t.Fatal("secret persisted")
	}
}

func TestParentAgentToolsRequestCancelExpiresChildCapability(t *testing.T) {
	_, _, child, sess, _, _ := parentAgentToolsTestPair(t)
	requestCtx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(requestCtx)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set(parentAgentToolsHeader, "turn-token")
	remove, err := child.acquireParentAgentTools(t.Context(), req, sess)
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	capability := child.parentAgentToolsForSession(sess)
	cancel()
	select {
	case <-capability.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("disconnected direct stream retained capability")
	}
	if child.parentAgentToolsForSession(sess) != nil {
		t.Fatal("cancelled capability usable")
	}
}

func TestParentAgentToolsStreamRejectsNonLoopbackHeader(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	s.parentAgentToolsURL = "http://127.0.0.1:1234"
	sess, _ := s.sessionManager.Create("build")
	sess.Metadata = map[string]interface{}{"source": "direct_agent_session", "parent_session_id": "parent"}
	if err := s.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/chat/stream", strings.NewReader(`{"message":"work"}`))
	req.RemoteAddr = "203.0.113.1:50000"
	req.Header.Set(parentAgentToolsHeader, "forged-token")
	// Forwarded headers must not turn a remote caller into a trusted host runtime.
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forged header status: %d %s", rec.Code, rec.Body.String())
	}
	if s.parentAgentToolsForSession(sess) != nil {
		t.Fatal("rejected header installed capability")
	}
	stored, _ := s.sessionManager.Get(sess.ID)
	if len(stored.Messages) != 0 {
		t.Fatal("rejected header mutated conversation")
	}
	_, release, err := s.sessionManager.AcquireRun(t.Context(), sess.ID)
	if err != nil {
		t.Fatalf("rejected request leaked run lease: %v", err)
	}
	release()
}

type parentToolsBlockingStreamWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (w *parentToolsBlockingStreamWriter) Write(body []byte) (int, error) {
	w.once.Do(func() { close(w.entered); <-w.resume })
	return w.ResponseRecorder.Write(body)
}

func TestParentAgentToolsStreamLeaseAndSuccessiveCleanup(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	s.parentAgentToolsURL = "http://127.0.0.1:1234"
	sess, _ := s.sessionManager.Create("build")
	// A nonexistent direct target ends the accepted stream without an LLM call,
	// after the initial status write where the test holds its run lease open.
	sess.Metadata = map[string]interface{}{"source": "direct_agent_session", "parent_session_id": "parent", "unified_agent_id": "missing-test-agent"}
	if err := s.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"first-turn", "second-turn"} {
		t.Run(token, func(t *testing.T) {
			request := func(token string) *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/chat/stream", strings.NewReader(`{"message":"work"}`))
				r.RemoteAddr = "127.0.0.1:50000"
				r.Header.Set(parentAgentToolsHeader, token)
				return r
			}
			writer := &parentToolsBlockingStreamWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), resume: make(chan struct{})}
			done := make(chan struct{})
			go func() { defer close(done); s.router.ServeHTTP(writer, request(token)) }()
			defer func() { close(writer.resume); <-done }()
			select {
			case <-writer.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("stream never acquired capability")
			}
			owner := s.parentAgentToolsForSession(sess)
			if owner == nil || owner.token != token {
				t.Fatal("owner capability missing")
			}
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, request("contending-token"))
			if rec.Code != http.StatusConflict {
				t.Fatalf("concurrent stream status: %d %s", rec.Code, rec.Body.String())
			}
			if s.parentAgentToolsForSession(sess) != owner || owner.ctx.Err() != nil {
				t.Fatal("rejected stream replaced or revoked active capability")
			}
		})
		s.parentAgentToolsMu.Lock()
		remaining := len(s.parentAgentTools)
		s.parentAgentToolsMu.Unlock()
		if remaining != 0 {
			t.Fatal("finished stream retained capability")
		}
	}
	s.sessionSummaryWG.Wait()
}

func TestParentAgentToolsInflightDelegationCancellation(t *testing.T) {
	for _, revokeTurn := range []bool{false, true} {
		t.Run(fmt.Sprintf("revoke=%t", revokeTurn), func(t *testing.T) {
			parent, _, child, sess, ctx, remove := parentAgentToolsTestPair(t)
			started, stopped := make(chan struct{}), make(chan struct{})
			parent.toolManager.Register(&bridgeDelegationProbe{name: "delegate_to_agent", execute: func(callCtx context.Context, _ json.RawMessage) (*tools.Result, error) {
				close(started)
				<-callCtx.Done()
				close(stopped)
				return nil, callCtx.Err()
			}})
			requestCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(requestCtx)
			req.RemoteAddr = "127.0.0.1:50000"
			req.Header.Set(parentAgentToolsHeader, child.parentAgentToolsForSession(sess).token)
			remove() // Replace the fixture's initial turn with a disconnectable turn.
			cleanup, err := child.acquireParentAgentTools(t.Context(), req, sess)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			manager := child.toolManagerForSession(sess)
			finished := make(chan error, 1)
			go func() {
				_, err := manager.Execute(ctx, "delegate_to_agent", json.RawMessage(`{"agent_id":"reviewer","task":"wait"}`))
				finished <- err
			}()
			select {
			case <-started:
			case err := <-finished:
				t.Fatalf("forwarded call failed before dispatch: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("parent delegation not entered")
			}
			if revokeTurn {
				cleanup()
			} else {
				cancel()
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("cancelled forwarding returned success")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("child forwarding did not cancel")
			}
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("parent delegation did not observe cancellation")
			}
			if _, err := manager.Execute(ctx, "delegate_to_agent", json.RawMessage(`{}`)); err == nil {
				t.Fatal("stale wrapper retained cancelled capability")
			}
		})
	}
}

func TestParentAgentToolsBoundsResponse(t *testing.T) {
	_, _, child, sess, ctx, _ := parentAgentToolsTestPair(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", mcpBridgeMaxBodyBytes+1))
	}))
	defer upstream.Close()
	child.parentAgentToolsMu.Lock()
	child.parentAgentTools[sess.ID].endpoint = upstream.URL
	child.parentAgentToolsMu.Unlock()
	result, err := child.toolManagerForSession(sess).Execute(ctx, "list_agents", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("oversized response must be rejected: %+v %v", result, err)
	}
}
