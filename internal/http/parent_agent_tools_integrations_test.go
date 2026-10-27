package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/websitecredentials"
)

func TestParentAgentToolsAppSignalUsesParentIntegration(t *testing.T) {
	parent, _, child, sess, ctx, remove := parentAgentToolsTestPair(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer parent-only-secret" {
			t.Error("AppSignal did not use parent credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var payload struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch payload.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"appsignal"}}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if payload.Params.Name != "get_applications" {
				t.Errorf("unexpected AppSignal tool: %s", payload.Params.Name)
			}
			calls.Add(1)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"parent-applications"}]}}`))
		default:
			t.Errorf("unexpected MCP method: %s", payload.Method)
		}
	}))
	defer upstream.Close()
	integration := &storage.Integration{
		ID: "parent-appsignal", Provider: "appsignal", Name: "Parent AppSignal", Enabled: true,
		Config:    map[string]string{"api_key": "parent-only-secret", "mcp_url": upstream.URL},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := parent.store.SaveIntegration(integration); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"operation":"call_tool","tool":"get_applications","integration_id":"parent-appsignal"}`)
	local, err := child.toolManager.Execute(ctx, "appsignal_query", args)
	if err != nil || local.Success || !strings.Contains(local.Error, "no enabled appsignal integrations") {
		t.Fatalf("child should have no AppSignal integration: %+v %v", local, err)
	}
	manager := child.toolManagerForSession(sess)
	for _, name := range []string{"appsignal_query", "parallel", "pipeline"} {
		input := args
		if name != "appsignal_query" {
			input = json.RawMessage(`{"steps":[{"tool":"appsignal_query","args":` + string(args) + `}]}`)
		}
		result, err := manager.Execute(ctx, name, input)
		if err != nil || !result.Success || !strings.Contains(result.Output, "parent-applications") || strings.Contains(result.Output, "parent-only-secret") {
			t.Fatalf("%s did not use parent integration: %+v %v", name, result, err)
		}
	}
	text, failed, rpcErr := child.mcpBridgeCallTool(ctx, sess, json.RawMessage(`{"name":"appsignal_query","arguments":`+string(args)+`}`))
	if failed || rpcErr != nil || !strings.Contains(text, "parent-applications") || calls.Load() != 4 {
		t.Fatalf("CLI integration forwarding: %s %v %v calls=%d", text, failed, rpcErr, calls.Load())
	}
	for _, name := range []string{"read", "bash", "browser_chrome", "stt"} {
		original, ok := child.toolManager.Get(name)
		wrapped, exists := manager.Get(name)
		if !ok || !exists || wrapped != original {
			t.Fatalf("local tool changed: %s", name)
		}
	}
	path := filepath.Join(t.TempDir(), "child-local.txt")
	if err := os.WriteFile(path, []byte("child-local-content"), 0600); err != nil {
		t.Fatal(err)
	}
	readArgs, _ := json.Marshal(map[string]string{"path": path})
	if result, err := manager.Execute(ctx, "read", readArgs); err != nil || !result.Success || !strings.Contains(result.Output, "child-local-content") {
		t.Fatalf("local read affected: %+v %v", result, err)
	}
	if integrations, err := child.store.ListIntegrations(); err != nil || len(integrations) != 0 {
		t.Fatalf("parent integrations copied to child: %+v %v", integrations, err)
	}
	if original, _ := child.toolManager.Get("appsignal_query"); original == nil {
		t.Fatal("shared registration lost")
	} else if _, wrapped := original.(*parentAgentTool); wrapped {
		t.Fatal("shared integration tool wrapped")
	}
	// Integration-specific permissions still execute on the parent.
	if result, err := manager.Execute(ctx, "appsignal_query", json.RawMessage(`{"operation":"call_tool","tool":"update_incidents"}`)); err != nil || result.Success || !strings.Contains(result.Error, "not allowed") {
		t.Fatalf("AppSignal read-only policy bypassed: %+v %v", result, err)
	}
	integration.Enabled = false
	if err := parent.store.SaveIntegration(integration); err != nil {
		t.Fatal(err)
	}
	if result, err := manager.Execute(ctx, "appsignal_query", args); err != nil || result.Success || !strings.Contains(result.Error, "no enabled appsignal integrations") {
		t.Fatalf("disabled integration remained usable: %+v %v", result, err)
	}
	remove()
	if _, err := manager.Execute(ctx, "appsignal_query", args); err == nil {
		t.Fatal("stale integration wrapper retained turn capability")
	}
	if calls.Load() != 4 {
		t.Fatal("denied integration request reached AppSignal")
	}
}

func TestParentAgentToolsIntegrationPolicies(t *testing.T) {
	parent, parentSess, child, sess, ctx, remove := parentAgentToolsTestPair(t)
	parent.toolManager.Register(&bridgeDelegationProbe{name: "appsignal_query", execute: func(context.Context, json.RawMessage) (*tools.Result, error) {
		return &tools.Result{Success: true, Output: "parent-integration"}, nil
	}})
	manager := child.toolManagerForSession(sess)
	wrongCtx := context.WithValue(ctx, "session_id", "another-child")
	if _, err := manager.Execute(wrongCtx, "appsignal_query", json.RawMessage(`{}`)); err == nil {
		t.Fatal("integration capability accepted another child session")
	}
	settings, _ := parent.store.GetSettings()
	settings[disabledToolsSettingKey] = `["appsignal_query"]`
	if err := parent.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if result, err := manager.Execute(ctx, "appsignal_query", json.RawMessage(`{}`)); err != nil || result.Success || !strings.Contains(result.Error, "tool not found") {
		t.Fatalf("parent disabled integration tool bypassed: %+v %v", result, err)
	}
	settings[disabledToolsSettingKey] = `[]`
	if err := parent.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	// Replace the child's turn with a fresh parent token to test parent revocation
	// independently of child-side cleanup.
	headers, revoke, err := parent.directAgentToolsHeaders(t.Context(), parentSess, agentdef.RuntimeHost)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	remove()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header = headers
	cleanup, err := child.acquireParentAgentTools(t.Context(), req, sess)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	manager = child.toolManagerForSession(sess)
	if result, err := manager.Execute(ctx, "appsignal_query", json.RawMessage(`{}`)); err != nil || !result.Success || result.Output != "parent-integration" {
		t.Fatalf("fresh parent token failed: %+v %v", result, err)
	}
	revoke()
	if _, err := manager.Execute(ctx, "appsignal_query", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("revoked parent token accepted: %v", err)
	}
	settings, _ = child.store.GetSettings()
	settings[disabledToolsSettingKey] = `["appsignal_query"]`
	if err := child.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	manager = child.toolManagerForSession(sess)
	if _, ok := manager.Get("appsignal_query"); ok {
		t.Fatal("child-disabled integration tool reintroduced")
	}
	for _, name := range []string{"parallel", "pipeline"} {
		result, err := manager.Execute(ctx, name, json.RawMessage(`{"steps":[{"tool":"appsignal_query","args":{}}]}`))
		if err == nil && !strings.Contains(result.Output+result.Error, "tool not found") {
			t.Fatalf("%s bypassed disabled integration: %+v", name, result)
		}
	}
}

func TestParentAgentToolsIntegrationAllowlistAndDirectScope(t *testing.T) {
	_, _, child, sess, _, _ := parentAgentToolsTestPair(t)
	allowed := []string{
		"google_calendar_query", "google_calendar_create_event", "gmail_query", "gmail_create_draft",
		"jira_query", "circleci_query", "code_review", "bitbucket_create_pull_request", "appsignal_query",
		"sentry_query", "cloudflare_query", "brave_search_query", "telegram_send_message",
		"telegram_user_list_chats", "telegram_user_read_messages", "discord_send_message", "exa_search",
		"tavily_search", "perplexity_search", "leonardo_generate_image", "comfyui_generate_image",
		"comfyui_run_workflow", "blender_render", "blender_run_script", "meshy_generate_3d",
		"tripo_generate_3d", "openai_generate_image",
	}
	manager := child.toolManagerForSession(sess)
	for _, name := range allowed {
		tool, ok := manager.Get(name)
		if !ok || !parentAgentToolAllowed(name) {
			t.Fatalf("allowlist does not match registered integration: %s", name)
		}
		if _, wrapped := tool.(*parentAgentTool); !wrapped {
			t.Fatalf("integration not wrapped: %s", name)
		}
	}
	for _, name := range []string{"mcp_call", "sql_query", "bash", "browser_chrome", "fetch_url", "notify_webapp", "stt", "parallel", "pipeline", "functions.appsignal_query", "arbitrary"} {
		if parentAgentToolAllowed(name) {
			t.Fatalf("non-allowlisted parent tool: %s", name)
		}
	}
	for _, metadata := range []map[string]interface{}{
		nil,
		{"source": "delegate_to_agent", "parent_session_id": "parent"},
		{"source": "direct_agent_session", "sub_agent_id": "delegated"},
	} {
		other := &session.Session{ID: sess.ID, Metadata: metadata}
		tool, _ := child.toolManagerForSession(other).Get("appsignal_query")
		if _, wrapped := tool.(*parentAgentTool); wrapped {
			t.Fatalf("integration bridge enabled outside direct session: %+v", metadata)
		}
	}
	parent := newMCPBridgeTestServer(t)
	ordinary, _ := parent.sessionManager.Create("build")
	headers, revoke, err := parent.directAgentToolsHeaders(t.Context(), ordinary, agentdef.RuntimeDocker)
	if err != nil || len(headers) != 0 {
		t.Fatalf("direct Docker received host integration capability: %v %v", headers, err)
	}
	revoke()
}

func TestParentAgentToolsWebsiteCredentialsUseParentVault(t *testing.T) {
	parent, parentSess, child, sess, ctx, remove := parentAgentToolsTestPair(t)
	// Distinct vault roots, mirroring host children with their own DataPath.
	parentVault, childVault := websitecredentials.New(t.TempDir()), websitecredentials.New(t.TempDir())
	parent.toolManager.Register(&websiteCredentialsTool{store: parentVault})
	child.toolManager.Register(&websiteCredentialsTool{store: childVault})
	parentRecord, err := parentVault.Create("https://parent.example", "parent-user", "parent-secret")
	if err != nil {
		t.Fatal(err)
	}
	childRecord, err := childVault.Create("https://child.example", "child-user", "child-secret")
	if err != nil {
		t.Fatal(err)
	}
	getArgs, _ := json.Marshal(map[string]string{"operation": "get", "id": parentRecord.ID})
	if local, err := child.toolManager.Execute(ctx, "website_credentials", getArgs); err != nil || local.Success {
		t.Fatalf("child-local vault should not contain parent record: %+v %v", local, err)
	}
	manager := child.toolManagerForSession(sess)
	result, err := manager.Execute(ctx, "website_credentials", json.RawMessage(`{"operation":"list"}`))
	if err != nil || !result.Success || !strings.Contains(result.Output, parentRecord.ID) || strings.Contains(result.Output, childRecord.ID) {
		t.Fatalf("list did not use parent vault: %+v %v", result, err)
	}
	result, err = manager.Execute(ctx, "website_credentials", getArgs)
	if err != nil || !result.Success || !strings.Contains(result.Output, "parent-secret") {
		t.Fatalf("get did not use parent vault: %+v %v", result, err)
	}
	text, failed, rpcErr := child.mcpBridgeCallTool(ctx, sess, json.RawMessage(`{"name":"website_credentials","arguments":`+string(getArgs)+`}`))
	if failed || rpcErr != nil || !strings.Contains(text, "parent-secret") {
		t.Fatalf("CLI credential forwarding: %s %v %v", text, failed, rpcErr)
	}
	if original, _ := child.toolManager.Get("website_credentials"); original == nil {
		t.Fatal("shared registration lost")
	} else if _, wrapped := original.(*parentAgentTool); wrapped {
		t.Fatal("shared credential tool wrapped")
	}
	if list, err := childVault.List(); err != nil || len(list) != 1 || list[0].ID != childRecord.ID {
		t.Fatalf("child vault changed: %+v %v", list, err)
	}

	settings, _ := parent.store.GetSettings()
	settings[disabledToolsSettingKey] = `["website_credentials"]`
	if err := parent.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if result, err := manager.Execute(ctx, "website_credentials", getArgs); err != nil || result.Success || !strings.Contains(result.Error, "tool not found") {
		t.Fatalf("parent-disabled credential tool bypassed: %+v %v", result, err)
	}
	settings[disabledToolsSettingKey] = `[]`
	if err := parent.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}

	headers, revoke, err := parent.directAgentToolsHeaders(t.Context(), parentSess, agentdef.RuntimeHost)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	remove()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header = headers
	cleanup, err := child.acquireParentAgentTools(t.Context(), req, sess)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	manager = child.toolManagerForSession(sess)
	if result, err := manager.Execute(ctx, "website_credentials", getArgs); err != nil || !result.Success {
		t.Fatalf("fresh parent token failed: %+v %v", result, err)
	}
	revoke()
	if result, err := manager.Execute(ctx, "website_credentials", getArgs); err == nil || (result != nil && strings.Contains(result.Output, "parent-secret")) {
		t.Fatalf("revoked parent token accepted: %+v %v", result, err)
	}

	settings, _ = child.store.GetSettings()
	settings[disabledToolsSettingKey] = `["website_credentials"]`
	if err := child.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if _, ok := child.toolManagerForSession(sess).Get("website_credentials"); ok {
		t.Fatal("child-disabled credential tool reintroduced")
	}
}
