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

	"github.com/A2gent/brute/internal/session"
)

func newMCPBridgeTestServer(t *testing.T) *Server {
	t.Helper()
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

func TestMCPBridgeEnabledByDefault(t *testing.T) {
	server := newMCPBridgeTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	cfg, revoke, err := server.claudecliMCPBridgeHook(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	if revoke == nil {
		t.Fatal("expected bridge token revoke callback")
	}
	defer revoke()
	if cfg == "" {
		t.Fatal("expected bridge config without feature flags")
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
	if !names["delegate_to_agent"] {
		t.Fatal("delegation tools must be exposed over the bridge")
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

func TestMCPBridgeQuestionParksSession(t *testing.T) {
	server := newMCPBridgeTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token, revoke := server.mustMintMCPBridgeToken(t, sess.ID)
	defer revoke()

	_, resp := serveMCPBridge(t, server, newMCPBridgeRequest(t, sess.ID, token,
		mcpBridgeRPC(t, 7, "tools/call", map[string]interface{}{"name": "question", "arguments": mcpBridgeQuestionArgs()})))
	text, isError := mcpBridgeCallResultText(t, resp)
	if isError || text != mcpBridgeQuestionParked {
		t.Fatalf("expected immediate park result, got %q (error=%v)", text, isError)
	}
	got, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != session.StatusInputRequired {
		t.Fatalf("status = %q, want input_required", got.Status)
	}
	q, err := server.sessionManager.GetPendingQuestion(sess.ID)
	if err != nil || q == nil || q.Header != "Pick a color" || len(q.Options) != 2 {
		t.Fatalf("pending question not persisted: %#v err=%v", q, err)
	}
	if pending := server.approvalBroker.PendingForSession(sess.ID); len(pending) != 0 {
		t.Fatalf("question must not hold an approval, got %d", len(pending))
	}
}

func TestAnswerQuestionWaitsForParkingRunThenResumes(t *testing.T) {
	server := newMCPBridgeTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := server.sessionManager.SetPendingQuestion(sess.ID, &session.QuestionData{Question: "Which color?"}); err != nil {
		t.Fatal(err)
	}
	if err := server.sessionManager.SetSessionStatus(sess.ID, string(session.StatusInputRequired)); err != nil {
		t.Fatal(err)
	}
	runID := server.registerActiveSessionRun(sess.ID, func() {})
	go func() {
		time.Sleep(150 * time.Millisecond)
		server.unregisterActiveSessionRun(sess.ID, runID)
	}()

	started := time.Now()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/answer", bytes.NewBufferString(`{"answer":"Use green"}`))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("answer status = %d (body %q)", rec.Code, rec.Body.String())
	}
	if time.Since(started) < 100*time.Millisecond {
		t.Fatal("answer did not wait for the parking run to end")
	}
	got, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := got.GetLastMessage()
	found := false
	for _, m := range got.Messages {
		if m.Role == "user" && m.Content == "Use green" {
			found = true
		}
	}
	if !found || last == nil {
		t.Fatalf("answer not recorded as user message: %#v", got.Messages)
	}
	if _, ok := got.Metadata["pending_question"]; ok {
		t.Fatal("pending question was not cleared")
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
