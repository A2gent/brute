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
	headers, revoke, err := parent.directAgentToolsHeaders(t.Context(), ordinary, agentdef.RuntimeRemote)
	if err != nil || len(headers) != 0 {
		t.Fatalf("non-child runtime received parent tools capability: %v %v", headers, err)
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

func TestParentAgentToolsChromeExtensionUsesParentBridge(t *testing.T) {
	parent, _, child, sess, ctx, _ := parentAgentToolsTestPair(t)
	// The extension only connects to the parent's port; the child bridge stays empty.
	parentHTTP := httptest.NewServer(parent.router)
	t.Cleanup(parentHTTP.Close)
	wsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	registerChromeExtensionWSTestPage(t, wsCtx, parent, parentHTTP, chromeExtensionRegisterRequest{
		PageID: "parent-page", ClientID: "client-1",
		Page: chromeExtensionPageSnapshot{URL: "https://example.test/parent", Title: "Parent", VisibilityState: "visible"},
	})
	listArgs := json.RawMessage(`{"action":"list_pages"}`)
	if local, err := child.toolManager.Execute(ctx, "chrome_extension", listArgs); err != nil || strings.Contains(local.Output, "parent-page") {
		t.Fatalf("child-local bridge should not see parent page: %+v %v", local, err)
	}
	result, err := child.toolManagerForSession(sess).Execute(ctx, "chrome_extension", listArgs)
	if err != nil || !result.Success || !strings.Contains(result.Output, "parent-page") {
		t.Fatalf("list_pages did not use parent bridge: %+v %v", result, err)
	}
}

func TestParentAgentToolsRecurringJobsUseParentStore(t *testing.T) {
	parent, _, child, sess, ctx, _ := parentAgentToolsTestPair(t)
	// Caesar reads jobs from the parent; a child-local job would be invisible there.
	now := time.Now()
	if err := parent.store.SaveJob(&storage.RecurringJob{ID: "parent-job", Name: "Parent loop", ScheduleCron: "0 0 * * *", Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	listArgs := json.RawMessage(`{"action":"list"}`)
	if local, err := child.toolManager.Execute(ctx, "recurring_jobs", listArgs); err != nil || strings.Contains(local.Output, "parent-job") {
		t.Fatalf("child-local store should not see parent job: %+v %v", local, err)
	}
	result, err := child.toolManagerForSession(sess).Execute(ctx, "recurring_jobs", listArgs)
	if err != nil || !result.Success || !strings.Contains(result.Output, "parent-job") {
		t.Fatalf("list did not use parent store: %+v %v", result, err)
	}
}

func TestRecurringJobsParentScopeDefaultsToChildAgent(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	sess, _ := s.sessionManager.Create("build")
	sess.Metadata = map[string]interface{}{"unified_agent_id": "child-agent"}
	if err := s.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	tool := newRecurringJobsTool(s)
	ctx := context.WithValue(t.Context(), "session_id", sess.ID)
	scoped := context.WithValue(ctx, parentAgentToolsScopeKey{}, true)

	job := &storage.RecurringJob{}
	tool.applyParentScopeAgentDefault(ctx, job)
	if job.UnifiedAgentID != "" {
		t.Fatalf("unscoped call must keep the default agent: %+v", job)
	}
	tool.applyParentScopeAgentDefault(scoped, job)
	if job.UnifiedAgentID != "child-agent" {
		t.Fatalf("child job should run as the child agent: %+v", job)
	}
	explicit := &storage.RecurringJob{LaunchAgentID: "other"}
	tool.applyParentScopeAgentDefault(scoped, explicit)
	if explicit.UnifiedAgentID != "" {
		t.Fatalf("explicit target overridden: %+v", explicit)
	}
}

func TestEveryRegisteredToolDeclaresPlacement(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	sess, _ := s.sessionManager.Create("build")
	// A tool missing from toolPlacements would silently run against the
	// child's own store and connections. Classify it there when adding one.
	for _, def := range s.toolManagerForSession(sess).GetDefinitions() {
		if _, declared := toolPlacementFor(def.Name); !declared {
			t.Errorf("tool %q has no placement in toolPlacements", def.Name)
		}
	}
}

func TestParentAgentToolsTasksUseParentStore(t *testing.T) {
	parent, parentSess, child, sess, ctx, _ := parentAgentToolsTestPair(t)
	root := t.TempDir()
	projectID := "parent-tasks"
	if err := parent.store.SaveProject(&storage.Project{ID: projectID, Name: "Parent", Folder: &root, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	parentSess.ProjectID = &projectID
	if err := parent.sessionManager.Save(parentSess); err != nil {
		t.Fatal(err)
	}
	created, err := child.toolManagerForSession(sess).Execute(ctx, "tasks", json.RawMessage(`{"action":"create","title":"Child follow-up"}`))
	if err != nil || !created.Success {
		t.Fatalf("create: %+v %v", created, err)
	}
	tasks, err := parent.store.ListTasks(projectID)
	if err != nil || len(tasks) != 1 || tasks[0].Title != "Child follow-up" {
		t.Fatalf("task not stored in parent project: %+v %v", tasks, err)
	}
}

func TestRecurringJobsRefusesLocalStoreInChildRuntime(t *testing.T) {
	t.Setenv(storage.ChildRuntimeEnv, "1")
	s := newMCPBridgeTestServer(t)
	sess, _ := s.sessionManager.Create("build")
	ctx := context.WithValue(t.Context(), "session_id", sess.ID)
	// Without the parent route (e.g. delegated sessions) a job saved here would
	// never run: children have no scheduler.
	result, err := newRecurringJobsTool(s).Execute(ctx, json.RawMessage(`{"action":"list"}`))
	if err != nil || result.Success || !strings.Contains(result.Error, "parent") {
		t.Fatalf("child-local recurring_jobs should refuse: %+v %v", result, err)
	}
}

func TestDockerParentToolsCapabilityIsPathFreeAndNetworkReachable(t *testing.T) {
	parent := newMCPBridgeTestServer(t)
	sess, _ := parent.sessionManager.Create("build")
	headers, revoke, err := parent.directAgentToolsHeaders(t.Context(), sess, agentdef.RuntimeDocker)
	if err != nil || headers.Get(parentAgentToolsHeader) == "" {
		t.Fatalf("Docker child got no capability: %v %v", headers, err)
	}
	defer revoke()
	dockerToken := headers.Get(parentAgentToolsHeader)
	_, tokenCtx, _ := parent.mcpBridge.resolve(dockerToken)
	// The container sees its project as /workspace; host-path tools stay local.
	if !mcpBridgeToolExposedContext(tokenCtx, "tasks") || !mcpBridgeToolExposedContext(tokenCtx, "jira_query") {
		t.Fatal("path-free parent tools must be exposed to Docker children")
	}
	if mcpBridgeToolExposedContext(tokenCtx, "openai_generate_image") || mcpBridgeToolExposedContext(tokenCtx, "bash") {
		t.Fatal("host-path or child-local tools exposed to Docker children")
	}

	fromDockerNetwork := func(token string) int {
		req := newMCPBridgeRequest(t, sess.ID, token, mcpBridgeRPC(t, 1, "tools/list", nil))
		req.RemoteAddr = "172.17.0.2:51000" // Linux Docker bridge, not loopback
		rec := httptest.NewRecorder()
		parent.router.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := fromDockerNetwork(dockerToken); code != http.StatusOK {
		t.Fatalf("Docker capability rejected from Docker network: %d", code)
	}
	hostHeaders, hostRevoke, err := parent.directAgentToolsHeaders(t.Context(), sess, agentdef.RuntimeHost)
	if err != nil {
		t.Fatal(err)
	}
	defer hostRevoke()
	for _, token := range []string{hostHeaders.Get(parentAgentToolsHeader), "bogus", ""} {
		if code := fromDockerNetwork(token); code != http.StatusForbidden {
			t.Fatalf("non-Docker token %q accepted off loopback: %d", token, code)
		}
	}
}

func TestDockerChildRoutesOnlyPathFreeToolsToParent(t *testing.T) {
	child := newMCPBridgeTestServer(t)
	child.parentAgentToolsURL = trustedParentAgentToolsURL("http://host.docker.internal:5445")
	if child.parentAgentToolsURL == "" || trustedParentAgentToolsURL("http://evil.example:5445") != "" {
		t.Fatalf("Docker parent origin trust is wrong: %q", child.parentAgentToolsURL)
	}
	sess, _ := child.sessionManager.Create("build")
	sess.Metadata = map[string]interface{}{"source": "direct_agent_session", "parent_session_id": "parent-1"}
	if err := child.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "192.168.65.1:50000" // parent via the published port
	req.Header.Set(parentAgentToolsHeader, "token")
	remove, err := child.acquireParentAgentTools(t.Context(), req, sess)
	if err != nil {
		t.Fatalf("Docker child refused the parent's turn: %v", err)
	}
	defer remove()
	manager := child.toolManagerForSession(sess)
	for name, routed := range map[string]bool{"tasks": true, "chrome_extension": true, "openai_generate_image": false, "bash": false} {
		tool, ok := manager.Get(name)
		if !ok {
			continue
		}
		if _, wrapped := tool.(*parentAgentTool); wrapped != routed {
			t.Errorf("%s routed to parent = %v, want %v", name, wrapped, routed)
		}
	}
}

func TestDockerChildRuntimeEnvAndStaleness(t *testing.T) {
	labels, env := map[string]string{}, map[string]string(nil)
	applyDockerChildRuntime(labels, &env, 5445)
	if env[parentAgentToolsURLEnv] != "http://host.docker.internal:5445" || env[storage.ChildRuntimeEnv] != "1" {
		t.Fatalf("child env: %#v", env)
	}
	if dockerChildRuntimeStale(labels, 5445) {
		t.Fatal("fresh container reported stale")
	}
	if !dockerChildRuntimeStale(map[string]string{}, 5445) || !dockerChildRuntimeStale(labels, 5446) {
		t.Fatal("pre-existing or other-parent container not recreated")
	}
}
