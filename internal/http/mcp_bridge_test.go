package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/approval"
)

func newMCPBridgeTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv(mcpBridgeEnabledEnv, "true")
	server, _ := newBruteHTTPProxyTestServer(t)
	return server
}

func (s *Server) mustMintMCPBridgeToken(t *testing.T, sessionID string) (string, func()) {
	t.Helper()
	token, revoke, err := s.mcpBridge.mint(sessionID)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return token, revoke
}

func newMCPBridgeRequest(t *testing.T, sessionID, token string, payload interface{}) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp/sessions/"+sessionID, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:51000"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func mcpBridgeRPC(t *testing.T, id int, method string, params interface{}) map[string]interface{} {
	t.Helper()
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		msg["params"] = params
	}
	return msg
}

type mcpBridgeTestResponse struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      int                    `json:"id"`
	Result  map[string]interface{} `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func serveMCPBridge(t *testing.T, server *Server, req *http.Request) (int, mcpBridgeTestResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	var resp mcpBridgeTestResponse
	if rec.Body.Len() > 0 && rec.Header().Get("Content-Type") == "application/json" {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v (body %q)", err, rec.Body.String())
		}
	}
	return rec.Code, resp
}

func mcpBridgeCallResultText(t *testing.T, resp mcpBridgeTestResponse) (string, bool) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	content, ok := resp.Result["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatalf("result.content missing: %#v", resp.Result)
	}
	first, ok := content[0].(map[string]interface{})
	if !ok {
		t.Fatalf("result.content[0] malformed: %#v", content[0])
	}
	text, _ := first["text"].(string)
	isError, _ := resp.Result["isError"].(bool)
	return text, isError
}

func TestMCPBridgeDisabledReturns404(t *testing.T) {

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token, revoke := server.mustMintMCPBridgeToken(t, sess.ID)
	defer revoke()

	req := newMCPBridgeRequest(t, sess.ID, token, mcpBridgeRPC(t, 1, "initialize", nil))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestMCPBridgeInitializeAndToolsList(t *testing.T) {

	server := newMCPBridgeTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token, revoke := server.mustMintMCPBridgeToken(t, sess.ID)
	defer revoke()

	code, resp := serveMCPBridge(t, server, newMCPBridgeRequest(t, sess.ID, token,
		mcpBridgeRPC(t, 1, "initialize", map[string]interface{}{"protocolVersion": "2025-03-26"})))
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := resp.Result["protocolVersion"]; got != "2025-03-26" {
		t.Fatalf("protocolVersion = %#v", got)
	}
	serverInfo, ok := resp.Result["serverInfo"].(map[string]interface{})
	if !ok || serverInfo["name"] == "" {
		t.Fatalf("serverInfo missing: %#v", resp.Result)
	}

	code, resp = serveMCPBridge(t, server, newMCPBridgeRequest(t, sess.ID, token, mcpBridgeRPC(t, 2, "tools/list", nil)))
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	toolsList, ok := resp.Result["tools"].([]interface{})
	if !ok {
		t.Fatalf("tools missing: %#v", resp.Result)
	}
	names := map[string]bool{}
	for _, raw := range toolsList {
		entry, _ := raw.(map[string]interface{})
		name, _ := entry["name"].(string)
		names[name] = true
		if _, schemaOK := entry["inputSchema"]; !schemaOK {
			t.Fatalf("tool %q has no inputSchema", name)
		}
	}
	if !names["question"] {
		t.Fatal("expected question tool to be exposed")
	}
	if names["bash"] {
		t.Fatal("bash must not be exposed over the bridge")
	}
	if names["delegate_to_agent"] {
		t.Fatal("delegation tools must not be exposed over the bridge")
	}
	if names["mcp_call"] {
		t.Fatal("mcp meta tools must not be exposed over the bridge")
	}
}

func TestMCPBridgeToolsListOmitsDisabledTools(t *testing.T) {

	server := newMCPBridgeTestServer(t)
	settings, err := server.store.GetSettings()
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	settings[disabledToolsSettingKey] = `["question"]`
	if err := server.store.SaveSettings(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token, revoke := server.mustMintMCPBridgeToken(t, sess.ID)
	defer revoke()

	_, resp := serveMCPBridge(t, server, newMCPBridgeRequest(t, sess.ID, token, mcpBridgeRPC(t, 1, "tools/list", nil)))
	toolsList, _ := resp.Result["tools"].([]interface{})
	for _, raw := range toolsList {
		entry, _ := raw.(map[string]interface{})
		if entry["name"] == "question" {
			t.Fatal("disabled tool question must not be listed")
		}
	}

	// A direct call to a non-exposed tool returns an error result, not a bypass.
	_, callResp := serveMCPBridge(t, server, newMCPBridgeRequest(t, sess.ID, token,
		mcpBridgeRPC(t, 2, "tools/call", map[string]interface{}{"name": "bash", "arguments": map[string]interface{}{"command": "ls"}})))
	text, isError := mcpBridgeCallResultText(t, callResp)
	if !isError {
		t.Fatalf("expected error result calling excluded tool, got %q", text)
	}
}

func TestMCPBridgeTokenScoping(t *testing.T) {

	server := newMCPBridgeTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	other, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}
	token, revoke := server.mustMintMCPBridgeToken(t, sess.ID)

	// Token bound to a different session is rejected.
	req := newMCPBridgeRequest(t, other.ID, token, mcpBridgeRPC(t, 1, "initialize", nil))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mismatched session status = %d, want 403", rec.Code)
	}

	// Missing token is rejected.
	req = newMCPBridgeRequest(t, sess.ID, "", mcpBridgeRPC(t, 1, "initialize", nil))
	rec = httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", rec.Code)
	}

	// Revoked token is rejected.
	revoke()
	req = newMCPBridgeRequest(t, sess.ID, token, mcpBridgeRPC(t, 1, "initialize", nil))
	rec = httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d, want 401", rec.Code)
	}

	// Non-loopback peers are rejected even with a valid token.
	token2, revoke2 := server.mustMintMCPBridgeToken(t, sess.ID)
	defer revoke2()
	req = newMCPBridgeRequest(t, sess.ID, token2, mcpBridgeRPC(t, 1, "initialize", nil))
	req.RemoteAddr = "10.0.0.5:8080"
	rec = httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback status = %d, want 403", rec.Code)
	}
}

func TestMCPBridgeCallToolDirect(t *testing.T) {

	server := newMCPBridgeTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token, revoke := server.mustMintMCPBridgeToken(t, sess.ID)
	defer revoke()

	code, resp := serveMCPBridge(t, server, newMCPBridgeRequest(t, sess.ID, token,
		mcpBridgeRPC(t, 1, "tools/call", map[string]interface{}{"name": "current_time", "arguments": map[string]interface{}{}})))
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	text, isError := mcpBridgeCallResultText(t, resp)
	if isError {
		t.Fatalf("expected success, got error text %q", text)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatal("expected non-empty tool output")
	}
}

func startMCPBridgeQuestion(t *testing.T, server *Server, sessionID, token string, args map[string]interface{}) chan mcpBridgeTestResponse {
	t.Helper()
	responses := make(chan mcpBridgeTestResponse, 1)
	go func() {
		_, resp := serveMCPBridge(t, server, newMCPBridgeRequest(t, sessionID, token,
			mcpBridgeRPC(t, 7, "tools/call", map[string]interface{}{"name": "question", "arguments": args})))
		responses <- resp
	}()
	waitForApprovalPending(t, server.approvalBroker, 1)
	return responses
}

func awaitMCPBridgeResponse(t *testing.T, responses <-chan mcpBridgeTestResponse) mcpBridgeTestResponse {
	t.Helper()
	select {
	case resp := <-responses:
		return resp
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for MCP bridge response")
		return mcpBridgeTestResponse{}
	}
}

func mcpBridgeQuestionArgs() map[string]interface{} {
	return map[string]interface{}{
		"question": "Which color should I use?",
		"header":   "Pick a color",
		"options": []map[string]interface{}{
			{"label": "Blue", "description": "calm"},
			{"label": "Red", "description": "loud"},
		},
		"custom": true,
	}
}

func TestMCPBridgeQuestionBlocksAndReturnsAnswer(t *testing.T) {

	server := newMCPBridgeTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token, revoke := server.mustMintMCPBridgeToken(t, sess.ID)
	defer revoke()
	events := mustSubscribe(t, server, sess.ID)

	responses := startMCPBridgeQuestion(t, server, sess.ID, token, mcpBridgeQuestionArgs())

	// Caesar sees a question approval with the raw question args as input.
	required := waitForStreamApprovalEvent(t, events, "permission_required")
	if required.Approval == nil {
		t.Fatal("permission_required carried no approval")
	}
	if required.Approval.Kind != "question" {
		t.Fatalf("approval kind = %q, want question", required.Approval.Kind)
	}
	if required.Approval.Input["question"] != "Which color should I use?" {
		t.Fatalf("approval input question = %#v", required.Approval.Input["question"])
	}
	if len(required.Approval.Questions) != 1 {
		t.Fatalf("approval questions = %#v", required.Approval.Questions)
	}
	q := required.Approval.Questions[0]
	if q.Header != "Pick a color" || len(q.Options) != 2 || !q.Custom {
		t.Fatalf("parsed question mismatch: %+v", q)
	}

	// Resolve the way Caesar's approval endpoint does.
	server.setApprovalResolvePayload(required.Approval.RequestID, approvalResolvePayload{
		Answers: map[string]string{"Which color should I use?": "Blue"},
	})
	if err := server.approvalBroker.Resolve(required.Approval.RequestID, sess.ID, approval.DecisionAllowOnce); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	resp := awaitMCPBridgeResponse(t, responses)
	text, isError := mcpBridgeCallResultText(t, resp)
	if isError {
		t.Fatalf("expected success, got %q", text)
	}
	if !strings.Contains(text, "Blue") {
		t.Fatalf("expected answer in tool result, got %q", text)
	}
}

func TestMCPBridgeQuestionTimeout(t *testing.T) {

	t.Setenv(mcpBridgeTimeoutEnv, "50ms")
	server := newMCPBridgeTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token, revoke := server.mustMintMCPBridgeToken(t, sess.ID)
	defer revoke()

	responses := startMCPBridgeQuestion(t, server, sess.ID, token, mcpBridgeQuestionArgs())
	resp := awaitMCPBridgeResponse(t, responses)
	text, isError := mcpBridgeCallResultText(t, resp)
	if !isError {
		t.Fatalf("expected timeout error result, got %q", text)
	}
	if !strings.Contains(strings.ToLower(text), "timed out") {
		t.Fatalf("expected timeout message, got %q", text)
	}
	if pending := server.approvalBroker.PendingForSession(sess.ID); len(pending) != 0 {
		t.Fatalf("expected no pending approvals after timeout, got %d", len(pending))
	}
}

func TestMCPBridgeRevokeCancelsPendingQuestion(t *testing.T) {

	server := newMCPBridgeTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token, revoke := server.mustMintMCPBridgeToken(t, sess.ID)

	responses := startMCPBridgeQuestion(t, server, sess.ID, token, mcpBridgeQuestionArgs())
	revoke()

	resp := awaitMCPBridgeResponse(t, responses)
	text, isError := mcpBridgeCallResultText(t, resp)
	if !isError {
		t.Fatalf("expected cancellation error result, got %q", text)
	}
	if !strings.Contains(strings.ToLower(text), "cancel") {
		t.Fatalf("expected cancellation message, got %q", text)
	}
}

func TestAnswerQuestionBridgePendingSkipsResume(t *testing.T) {

	server := newMCPBridgeTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token, revoke := server.mustMintMCPBridgeToken(t, sess.ID)
	defer revoke()

	responses := startMCPBridgeQuestion(t, server, sess.ID, token, mcpBridgeQuestionArgs())

	body := bytes.NewBufferString(`{"answer":"Use green"}`)
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/answer", body)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("answer status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	resp := awaitMCPBridgeResponse(t, responses)
	text, isError := mcpBridgeCallResultText(t, resp)
	if isError {
		t.Fatalf("expected success, got %q", text)
	}
	if !strings.Contains(text, "Use green") {
		t.Fatalf("expected composer answer in tool result, got %q", text)
	}

	// The fork must not spawn a resume run while the CLI subprocess owns the session.
	if got := server.activeSessionRunCount(sess.ID); got != 0 {
		t.Fatalf("active runs = %d, want 0 (resume must not fire for bridge questions)", got)
	}
}

func TestMCPBridgeHookDisabledWithoutFlag(t *testing.T) {

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	cfg, revoke, err := server.claudecliMCPBridgeHook(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	if cfg != "" || revoke != nil {
		t.Fatalf("expected empty config without flag, got %q", cfg)
	}
}

func TestMCPBridgeHookMintsScopedConfig(t *testing.T) {

	server := newMCPBridgeTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	cfgJSON, revoke, err := server.claudecliMCPBridgeHook(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	defer revoke()
	if cfgJSON == "" {
		t.Fatal("expected non-empty MCP config")
	}
	var cfg struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	entry, ok := cfg.MCPServers["a2gent"]
	if !ok {
		t.Fatalf("mcpServers.a2gent missing: %v", cfg.MCPServers)
	}
	if entry.Type != "http" || !strings.Contains(entry.URL, "/mcp/sessions/"+sess.ID) {
		t.Fatalf("unexpected server entry: %+v", entry)
	}
	token := strings.TrimPrefix(entry.Headers["Authorization"], "Bearer ")
	if token == "" {
		t.Fatal("bearer token missing from config")
	}
	bound, _, ok := server.mcpBridge.resolve(token)
	if !ok || bound != sess.ID {
		t.Fatalf("token not registered for session %s", sess.ID)
	}

	// Revocation invalidates the token.
	revoke()
	if _, _, ok := server.mcpBridge.resolve(token); ok {
		t.Fatal("token still valid after revoke")
	}
}
