package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/approval"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/tools"
)

func newMCPRelayPair(t *testing.T) (*Server, *Server, string, string) {
	t.Helper()
	parent := newMCPBridgeTestServer(t)
	child := newMCPBridgeTestServer(t)
	child.parentMCPKey = "runtime-a." + strings.Repeat("a", 64)
	old := runCommand
	runCommand = func(ctx context.Context, cmd string, args ...string) (string, error) {
		if cmd == "docker" && len(args) == 4 && args[0] == "inspect" && args[3] == "runtime-a" {
			return "true true " + mcpRelayKeyHash(child.parentMCPKey), nil
		}
		return "", fmt.Errorf("unexpected command %s %v", cmd, args)
	}
	t.Cleanup(func() { runCommand = old })
	endpoint := httptest.NewServer(parent.router)
	t.Cleanup(endpoint.Close)
	sess, err := child.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	return parent, child, endpoint.URL + "/v1", sess.ID
}

func openTestMCPRelay(t *testing.T, parent, child *Server, base, id string) (*mcpRelay, http.Header, func()) {
	t.Helper()
	headers, cleanup, err := child.prepareParentMCPRelay(base)(t.Context(), &llm.ChatRequest{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header = headers
	relay, err := parent.claimMCPRelay(req)
	if err != nil {
		t.Fatal(err)
	}
	return relay, headers, cleanup
}

func relayRPC(t *testing.T, relay *mcpRelay, method string, params interface{}) mcpBridgeTestResponse {
	t.Helper()
	raw, _ := json.Marshal(params)
	response := relay.call(t.Context(), &mcpBridgeRPCMessage{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: raw})
	data, _ := json.Marshal(response)
	var result mcpBridgeTestResponse
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMCPRelayChildContextRestrictionsAndNativeTools(t *testing.T) {
	parent, child, base, id := newMCPRelayPair(t)
	child.toolManager.Register(&bridgeDelegationProbe{name: "bridge_probe", execute: func(ctx context.Context, _ json.RawMessage) (*tools.Result, error) {
		if ctx.Value("session_id") != id || ctx.Value("projectID") != "child-project" {
			t.Errorf("lost child context: %v %v", ctx.Value("session_id"), ctx.Value("projectID"))
		}
		return &tools.Result{Success: true, Output: "child-only"}, nil
	}})
	sess, _ := child.sessionManager.Get(id)
	project := "child-project"
	sess.ProjectID = &project
	if err := child.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	settings, _ := child.store.GetSettings()
	settings[disabledToolsSettingKey] = `["current_time","write"]`
	if err := child.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	relay, _, _ := openTestMCPRelay(t, parent, child, base, id)
	if _, err := parent.sessionManager.Get(id); err == nil {
		t.Fatal("test accidentally has a parent session")
	}
	text, bad := mcpBridgeCallResultText(t, relayRPC(t, relay, "tools/call", map[string]interface{}{"name": "bridge_probe", "arguments": map[string]interface{}{}}))
	if bad || text != "child-only" {
		t.Fatalf("wrong tool result %q error=%v", text, bad)
	}
	listed := relayRPC(t, relay, "tools/list", nil)
	data, _ := json.Marshal(listed.Result)
	if strings.Contains(string(data), `"name":"write"`) || strings.Contains(string(data), `"name":"current_time"`) {
		t.Fatalf("disabled tool exposed: %s", data)
	}
	if !strings.Contains(string(data), `"name":"read"`) || strings.Contains(string(data), `"name":"parallel"`) {
		t.Fatalf("wrong native relay surface: %s", data)
	}
	_, bad = mcpBridgeCallResultText(t, relayRPC(t, relay, "tools/call", map[string]interface{}{"name": "write"}))
	if !bad {
		t.Fatal("disabled write was callable")
	}
	if mcpBridgeToolExposedContext(context.Background(), "read") {
		t.Fatal("host bridge changed")
	}
}

func TestMCPRelayRejectsCrossSessionAndRuntimeCredentials(t *testing.T) {
	parent, child, base, id := newMCPRelayPair(t)
	headers, cleanup, err := child.prepareParentMCPRelay(base)(t.Context(), &llm.ChatRequest{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, variant := range []string{"session", "runtime", "missing-auth", "missing-relay", "unknown-relay"} {
		t.Run(variant, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
			req.Header = headers.Clone()
			switch variant {
			case "session":
				req.Header.Set(mcpRelaySessionHeader, "another-session")
			case "runtime":
				req.Header.Set("Authorization", "Bearer runtime-b."+strings.Repeat("b", 64))
			case "missing-auth":
				req.Header.Del("Authorization")
			case "missing-relay":
				req.Header.Del(mcpRelayHeader)
			case "unknown-relay":
				req.Header.Set(mcpRelayHeader, "unknown")
			}
			rec := httptest.NewRecorder()
			parent.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("credential accepted: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header = headers
	relay, err := parent.claimMCPRelay(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.claimMCPRelay(req); err == nil {
		t.Fatal("relay replay accepted")
	}
	cleanup()
	select {
	case <-relay.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("disconnect did not revoke relay")
	}
	if _, err := parent.claimMCPRelay(req); err == nil {
		t.Fatal("revoked relay accepted")
	}
}

func TestMCPRelayInvalidBootstrapAndCLIBypass(t *testing.T) {
	parent, child, base, id := newMCPRelayPair(t)
	child.parentMCPKey = "runtime-a." + strings.Repeat("b", 64)
	// The expected registry hash is independent of the submitted key.
	original := runCommand
	runCommand = func(ctx context.Context, cmd string, args ...string) (string, error) {
		return "true true " + mcpRelayKeyHash("runtime-a."+strings.Repeat("a", 64)), nil
	}
	_, cleanup, err := child.prepareParentMCPRelay(base)(t.Context(), &llm.ChatRequest{SessionID: id})
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("invalid bootstrap accepted")
	}
	runCommand = original
	for _, provider := range []config.ProviderType{config.ProviderAnthropic, "anthropic:work", config.ProviderCursor, config.ProviderKimiCLI} {
		if err := parent.validateProxyCLIConfinement(provider, false); err == nil {
			t.Fatalf("unconfined CLI provider accepted: %s", provider)
		}
	}
	// Keep fallback validation deterministic on machines without Claude installed.
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://parent.invalid/v1")
	parent.config.HostClaudeCLI = true
	parent.config.FallbackAggregates = []config.FallbackAggregate{{ID: "relay", Chain: []config.FallbackChainNode{{Provider: "lmstudio", Model: "local"}, {Provider: "anthropic", Model: "sonnet"}}}}
	// Host mode bypasses the proxy shortcut for chain lookup, but Claude still
	// needs an executable. This binary is never executed by the validation test.
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "/bin/sh")
	if err := parent.validateProxyCLIConfinement("fallback_chain:relay", false); err == nil {
		t.Fatal("unconfined fallback Claude accepted")
	}
	if err := parent.validateProxyCLIConfinement("fallback_chain:relay", true); err != nil {
		t.Fatalf("confined fallback rejected: %v", err)
	}
	if err := parent.validateProxyCLIConfinement(config.ProviderAnthropic, true); err != nil {
		t.Fatal(err)
	}
}

func TestMCPRelayApprovalCancellationAndTokenRevocation(t *testing.T) {
	parent, child, base, id := newMCPRelayPair(t)
	relay, _, cleanup := openTestMCPRelay(t, parent, child, base, id)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), mcpRelayContextKey{}, relay))
	defer cancel()
	cfg, revoke, err := parent.claudecliMCPBridgeHook(ctx, relay.id)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	var config struct {
		Servers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cfg), &config); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(config.Servers["a2gent"].Headers["Authorization"], "Bearer ")
	wrong, _ := parent.sessionManager.Create("build")
	rec := httptest.NewRecorder()
	parent.router.ServeHTTP(rec, newMCPBridgeRequest(t, wrong.ID, token, mcpBridgeRPC(t, 1, "ping", nil)))
	code := rec.Code
	if code != 403 {
		t.Fatalf("remote token accepted for parent: %d", code)
	}
	responses := make(chan mcpBridgeTestResponse, 1)
	go func() {
		responses <- relayRPC(t, relay, "tools/call", map[string]interface{}{"name": "question", "arguments": map[string]interface{}{"question": "child approval?"}})
	}()
	waitForApprovalPending(t, child.approvalBroker, 1)
	if len(parent.approvalBroker.Pending()) != 0 {
		t.Fatal("approval ran on parent")
	}
	pending := child.approvalBroker.PendingForSession(id)
	if len(pending) != 1 {
		t.Fatal("approval not bound to child")
	}
	child.setApprovalResolvePayload(pending[0].ID, approvalResolvePayload{Message: "approved-child"})
	if err := child.approvalBroker.Resolve(pending[0].ID, id, approval.DecisionAllowOnce); err != nil {
		t.Fatal(err)
	}
	text, bad := mcpBridgeCallResultText(t, awaitMCPBridgeResponse(t, responses))
	if bad || text != "approved-child" {
		t.Fatalf("approval lost: %q", text)
	}
	go func() {
		responses <- relayRPC(t, relay, "tools/call", map[string]interface{}{"name": "question", "arguments": map[string]interface{}{"question": "cancel?"}})
	}()
	waitForApprovalPending(t, child.approvalBroker, 1)
	cleanup()
	select {
	case <-responses:
	case <-time.After(time.Second):
		t.Fatal("pending relay call leaked")
	}
	deadline := time.Now().Add(time.Second)
	for len(child.approvalBroker.PendingForSession(id)) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(child.approvalBroker.PendingForSession(id)) != 0 {
		t.Fatal("pending child approval leaked")
	}
	revoke()
	rec = httptest.NewRecorder()
	parent.router.ServeHTTP(rec, newMCPBridgeRequest(t, relay.id, token, mcpBridgeRPC(t, 1, "ping", nil)))
	code = rec.Code
	if code != 401 {
		t.Fatalf("revoked callback token accepted: %d", code)
	}
}

func TestMCPRelayRevokeCancelsOnlyOwningCallback(t *testing.T) {
	parent, child, base, id := newMCPRelayPair(t)
	relay, _, _ := openTestMCPRelay(t, parent, child, base, id)
	ctx := context.WithValue(t.Context(), mcpRelayContextKey{}, relay)
	cfg, revoke, err := parent.claudecliMCPBridgeHook(ctx, relay.id)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	var conf struct {
		Servers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cfg), &conf); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(conf.Servers["a2gent"].Headers["Authorization"], "Bearer ")
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		parent.router.ServeHTTP(rec, newMCPBridgeRequest(t, relay.id, token, mcpBridgeRPC(t, 1, "tools/call", map[string]interface{}{"name": "question", "arguments": map[string]interface{}{"question": "cancel callback?"}})))
	}()
	waitForApprovalPending(t, child.approvalBroker, 1)
	revoke()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revoke left callback blocked")
	}
	deadline := time.Now().Add(time.Second)
	for len(child.approvalBroker.PendingForSession(id)) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(child.approvalBroker.PendingForSession(id)) > 0 {
		t.Fatal("child approval survived callback revocation")
	}
	if response := relayRPC(t, relay, "ping", nil); response.Error != nil {
		t.Fatalf("revoking one callback broke relay: %+v", response)
	}
}

func TestMCPRelayRuntimeRotationAndQualifiedToolRejection(t *testing.T) {
	parent, child, base, id := newMCPRelayPair(t)
	headers, cleanup, err := child.prepareParentMCPRelay(base)(t.Context(), &llm.ChatRequest{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	original := runCommand
	for _, registry := range []string{"false true " + mcpRelayKeyHash(child.parentMCPKey), "true false " + mcpRelayKeyHash(child.parentMCPKey), "true true " + strings.Repeat("b", 64)} {
		runCommand = func(context.Context, string, ...string) (string, error) { return registry, nil }
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header = headers
		if _, err := parent.claimMCPRelay(req); err == nil {
			t.Fatalf("stopped/unmanaged/rotated runtime accepted: %s", registry)
		}
	}
	runCommand = original
	for _, name := range []string{"functions.parallel", "functions.mcp_call", "functions.bash"} {
		if mcpBridgeToolExposedContext(context.WithValue(t.Context(), mcpRelayChildKey{}, true), name) {
			t.Fatalf("qualified tool bypass: %s", name)
		}
	}
}

func TestMCPRelayLimitsAndCanceledDispatch(t *testing.T) {
	parent, child, base, id := newMCPRelayPair(t)
	var cleanups []func()
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()
	for i := 0; i < 4; i++ {
		_, cleanup, err := child.prepareParentMCPRelay(base)(t.Context(), &llm.ChatRequest{SessionID: id})
		if err != nil {
			t.Fatal(err)
		}
		cleanups = append(cleanups, cleanup)
	}
	if _, cleanup, err := child.prepareParentMCPRelay(base)(t.Context(), &llm.ChatRequest{SessionID: id}); err == nil {
		cleanup()
		t.Fatal("per-runtime relay quota bypassed")
	}
	// Capacity is reserved before the WebSocket upgrade, not after it.
	parent.mcpBridge.mu.Lock()
	for i := len(parent.mcpBridge.relays); i < 32; i++ {
		parent.mcpBridge.relays[fmt.Sprint(i)] = &mcpRelay{runtime: "other"}
	}
	parent.mcpBridge.mu.Unlock()
	if _, cleanup, err := child.prepareParentMCPRelay(base)(t.Context(), &llm.ChatRequest{SessionID: id}); err == nil {
		cleanup()
		t.Fatal("global relay quota bypassed")
	}
	for len(mcpRelayAuthSlots) < cap(mcpRelayAuthSlots) {
		mcpRelayAuthSlots <- struct{}{}
	}
	if _, ok := validMCPRelayRuntime(t.Context(), child.parentMCPKey); ok {
		t.Error("unbounded Docker auth check")
	}
	for len(mcpRelayAuthSlots) > 0 {
		<-mcpRelayAuthSlots
	}
	called := false
	child.toolManager.Register(&bridgeDelegationProbe{name: "mutation_probe", execute: func(context.Context, json.RawMessage) (*tools.Result, error) {
		called = true
		return &tools.Result{Success: true}, nil
	}})
	sess, _ := child.sessionManager.Get(id)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	params := json.RawMessage(`{"name":"mutation_probe","arguments":{}}`)
	response := child.dispatchMCPBridgeMessage(ctx, sess, &mcpBridgeRPCMessage{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: params})
	if response.Error == nil || called {
		t.Fatal("canceled queued dispatch executed a mutation")
	}
	_, bad, _ := child.mcpBridgeCallTool(ctx, sess, params)
	if !bad || called {
		t.Fatal("canceled direct tool call executed a mutation")
	}
}

func TestMCPRelayRetainsChildExecutionGuard(t *testing.T) {
	parent, child, base, id := newMCPRelayPair(t)
	ctx := tools.WithExecutionGuard(t.Context(), func(string) error { return fmt.Errorf("child run-mode denies execution") })
	headers, cleanup, err := child.prepareParentMCPRelay(base)(ctx, &llm.ChatRequest{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header = headers
	relay, err := parent.claimMCPRelay(req)
	if err != nil {
		t.Fatal(err)
	}
	text, bad := mcpBridgeCallResultText(t, relayRPC(t, relay, "tools/call", map[string]interface{}{"name": "current_time"}))
	if !bad || !strings.Contains(text, "child run-mode denies execution") {
		t.Fatalf("lost execution guard: %q", text)
	}
}

func TestMCPRelayRuntimeNameWithDots(t *testing.T) {
	key := "agent.claude." + strings.Repeat("a", 64)
	old := runCommand
	defer func() { runCommand = old }()
	runCommand = func(_ context.Context, _ string, args ...string) (string, error) {
		if args[len(args)-1] != "agent.claude" {
			t.Errorf("incorrect runtime name: %v", args)
		}
		return "true true " + mcpRelayKeyHash(key), nil
	}
	if runtime, ok := validMCPRelayRuntime(t.Context(), key); !ok || runtime != "agent.claude" {
		t.Fatalf("valid dotted runtime rejected: %q %v", runtime, ok)
	}
}
