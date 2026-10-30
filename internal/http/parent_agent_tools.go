package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

const (
	parentAgentToolsURLEnv = "A2GENT_HOST_PARENT_MCP_URL"
	parentAgentToolsHeader = "X-A2gent-Parent-Agent-Tools"
	delegationAsyncHeader  = "X-A2gent-Delegation-Async"
)

// This marker lives only in the parent's token context, never in RPC arguments.
type parentAgentToolsScopeKey struct{}
type parentAgentToolsWorkDirKey struct{}

// parentAgentToolsDockerKey marks a capability minted for a Docker child: it may
// arrive from the Docker network and is limited to tools without host paths.
type parentAgentToolsDockerKey struct{}

// parentAgentToolsDelegatedKey marks a capability minted for a delegate_to_agent
// child: delegation tools stay hidden so the one-hop limit cannot be bypassed
// by running delegate_to_agent on the (non-delegated) parent session.
type parentAgentToolsDelegatedKey struct{}

// dockerParentHost is how Docker children reach the parent; the parent sets it
// at spawn, so it is trusted like a loopback origin.
const dockerParentHost = "host.docker.internal"

type toolPlacement int

const (
	// placementChild runs in the child against its own workspace and session.
	placementChild toolPlacement = iota + 1
	// placementParent runs on the parent: the state is shown in Caesar or the
	// connection only reaches the parent (extension, vault, scheduler).
	placementParent
)

// toolPlacements classifies every built-in tool for host children; a test fails
// when a registered tool is missing. Integration tools are parent-placed via
// integrationtools.IsIntegrationBackedTool. Unknown runtime tools stay local.
var toolPlacements = map[string]toolPlacement{
	// Shared, user-visible state lives in the parent store.
	"list_agents":          placementParent,
	"delegate_to_agent":    placementParent,
	"wait_for_agent":       placementParent,
	"cancel_agent":         placementParent,
	"delegate_to_subagent": placementParent,
	"website_credentials":  placementParent, // host children have their own empty vault
	"chrome_extension":     placementParent, // the extension connects only to the parent port
	"recurring_jobs":       placementParent, // Caesar reads jobs from the parent store
	"tasks":                placementParent, // Caesar reads tasks from the parent store

	// Child-local by design: workspace, session, media and agent-internal tools.
	// Agent creation and external A2A stay local until children get a parent-scoped
	// Square identity; moving them would let a child create agents in the parent.
	"archived_sessions": placementChild, "bash": placementChild, "browser_chrome": placementChild,
	"code_execution": placementChild, "content_search": placementChild, "context_retrieve": placementChild,
	"create_local_docker_agents_bulk": placementChild, "create_local_docker_agents_from_yaml": placementChild,
	"current_time": placementChild, "delegate_to_external_agent": placementChild,
	"discover_external_agents": placementChild, "edit": placementChild, "fetch_url": placementChild,
	"file_search": placementChild, "filter": placementChild, "find_files": placementChild, "glob": placementChild,
	"grep": placementChild, "import_agent_definition_yaml": placementChild, "insert_lines": placementChild,
	"man": placementChild, "mcp_call": placementChild, "mcp_list_tools": placementChild, "mcp_manage": placementChild,
	"notify_webapp": placementChild, "parallel": placementChild, "pipeline": placementChild,
	"project_session_history": placementChild, "question": placementChild, "random_number": placementChild,
	"read": placementChild, "relevance_gate": placementChild, "replace_lines": placementChild,
	"session_task_progress": placementChild, "sql_query": placementChild, "stt": placementChild,
	"suggest_git_commit": placementChild, "suggest_session": placementChild, "take_camera_photo_tool": placementChild,
	"take_screenshot_tool": placementChild, "tts": placementChild, "wait_until": placementChild, "write": placementChild,
	"youtube_transcript": placementChild,
}

func toolPlacementFor(name string) (toolPlacement, bool) {
	if integrationtools.IsIntegrationBackedTool(name) {
		return placementParent, true
	}
	placement, ok := toolPlacements[name]
	return placement, ok
}

// hostPathTools are parent-placed tools that read or write files by host path.
// A Docker child sees its project as /workspace, so these stay in the container.
var hostPathTools = map[string]bool{
	"bitbucket_create_pull_request": true, // reads the local git checkout
	"blender_render":                true, "blender_run_script": true,
	"comfyui_generate_image": true, "comfyui_run_workflow": true,
	"leonardo_generate_image": true, "openai_generate_image": true,
	"meshy_generate_3d": true, "tripo_generate_3d": true,
}

func parentAgentToolAllowed(name string) bool {
	return parentAgentToolAllowedFor(name, false)
}

func parentAgentToolAllowedFor(name string, docker bool) bool {
	placement, _ := toolPlacementFor(name)
	return placement == placementParent && !(docker && hostPathTools[name])
}

// parentAgentToolAllowedForSession additionally keeps delegation tools local
// for delegated children, where the child-side policy denies them. Raw vault
// passwords also stay with user-started sessions: a delegated run is started by
// an LLM, so prompt injection could otherwise exfiltrate them.
func parentAgentToolAllowedForSession(name string, docker, delegated bool) bool {
	if delegated && (isDelegationTool(name) || name == "website_credentials") {
		return false
	}
	return parentAgentToolAllowedFor(name, docker)
}

// parentAgentToolsSource reports whether a child session may use a parent
// capability: direct sessions and delegate_to_agent sessions (one hop).
func parentAgentToolsSource(sess *session.Session) bool {
	switch metadataString(sess.Metadata["source"]) {
	case "direct_agent_session":
		return !isDelegatedSession(sess)
	case "delegate_to_agent":
		return strings.TrimSpace(metadataString(sess.Metadata["sub_agent_id"])) == ""
	}
	return false
}

// Only a spawn-provisioned loopback (or Docker host) origin is trusted; requests cannot supply a
// callback URL or credentials in it. Do not resolve DNS or follow redirects.
func trustedParentAgentToolsURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return ""
	}
	ip := net.ParseIP(u.Hostname())
	if u.Port() == "" || (u.Hostname() != dockerParentHost && (ip == nil || !ip.IsLoopback())) {
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}

type parentAgentToolsCapability struct {
	ctx      context.Context
	token    string
	endpoint string
}

func (s *Server) directAgentToolsHeaders(ctx context.Context, sess *session.Session, runtime string) (http.Header, func(), error) {
	if (runtime != agentdef.RuntimeHost && runtime != agentdef.RuntimeDocker) || isDelegatedSession(sess) {
		return nil, func() {}, nil
	}
	docker := runtime == agentdef.RuntimeDocker
	// Resolve from the parent's stored definition, never a child header, RPC
	// argument or docker_workspace metadata supplied by a caller. Match the
	// host spawn path: configured project root, or inherited process cwd.
	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	// Docker capabilities exclude host-path tools, so no workspace binding is needed.
	if !docker && strings.TrimSpace(metadataString(sess.Metadata["unified_agent_id"])) != "" {
		def, err := s.unifiedAgentDefinitionForSession(sess)
		if err != nil {
			return nil, nil, err
		}
		if def.Runtime.Type != agentdef.RuntimeHost {
			return nil, nil, fmt.Errorf("parent agent tools require a host definition")
		}
		projectID := ""
		if sess.ProjectID != nil {
			projectID = strings.TrimSpace(*sess.ProjectID)
		}
		workspace, err := s.resolveDockerWorkspaceBinding(def, projectID)
		if err != nil {
			return nil, nil, err
		}
		if len(workspace.ProjectMounts) > 0 {
			return nil, nil, fmt.Errorf("parent agent tools require a single host workspace")
		}
		if workspace.ProjectID != "" {
			workDir, err = s.resolveProjectRootFolder(workspace.ProjectID)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	return s.mintParentAgentToolsHeaders(ctx, sess.ID, workDir, docker, false, nil)
}

// delegatedAgentToolsHeaders gives a delegate_to_agent child the same parent
// integrations (Jira, Sentry, ...) as a direct session. WHY: a host child has
// its own empty store, so without this its integration tools always fail.
// The workspace comes from the delegated agent's resolved binding.
func (s *Server) delegatedAgentToolsHeaders(ctx context.Context, parentSessionID, runtime string, workspace *dockerWorkspaceBinding) (http.Header, func(), error) {
	parent, err := s.sessionManager.Get(parentSessionID)
	if err != nil || (runtime != agentdef.RuntimeHost && runtime != agentdef.RuntimeDocker) || isDelegatedSession(parent) {
		return nil, func() {}, nil
	}
	docker := runtime == agentdef.RuntimeDocker
	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	if !docker && workspace != nil && len(workspace.ProjectMounts) > 0 {
		// Same rule as direct sessions: host-path tools need one workspace root.
		return nil, nil, fmt.Errorf("parent agent tools require a single host workspace")
	}
	if !docker && workspace != nil && workspace.ProjectID != "" {
		if workDir, err = s.resolveProjectRootFolder(workspace.ProjectID); err != nil {
			return nil, nil, err
		}
	}
	scope := delegatedIntegrationScope(parent, workspace)
	return s.mintParentAgentToolsHeaders(ctx, parentSessionID, workDir, docker, true, &scope)
}

// integrationScope, when set, overrides the bound session's integration scope
// for tool calls made with this token.
func (s *Server) mintParentAgentToolsHeaders(ctx context.Context, sessionID, workDir string, docker, delegated bool, integrationScope *integrationtools.Scope) (http.Header, func(), error) {
	workDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(workDir)
	if err != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("parent agent tools workspace is unavailable")
	}
	scoped := context.WithValue(ctx, parentAgentToolsScopeKey{}, true)
	scoped = context.WithValue(scoped, parentAgentToolsWorkDirKey{}, workDir)
	if docker {
		scoped = context.WithValue(scoped, parentAgentToolsDockerKey{}, true)
	}
	if delegated {
		scoped = context.WithValue(scoped, parentAgentToolsDelegatedKey{}, true)
	}
	if integrationScope != nil {
		scoped = context.WithValue(scoped, parentAgentToolsIntegrationScopeKey{}, *integrationScope)
	}
	token, revoke, err := s.mcpBridge.mintWithContext(scoped, sessionID)
	if err != nil {
		return nil, nil, err
	}
	headers := make(http.Header)
	headers.Set(parentAgentToolsHeader, token)
	return headers, revoke, nil
}

// acquireParentAgentTools is called after the stream run lease is acquired.
// Capabilities are memory-only, and removed before releasing that lease.
func (s *Server) acquireParentAgentTools(ctx context.Context, r *http.Request, sess *session.Session) (func(), error) {
	token := r.Header.Get(parentAgentToolsHeader)
	if token == "" {
		return func() {}, nil
	}
	parentID, _ := sess.Metadata["parent_session_id"].(string)
	// A Docker child receives the parent's turn through its published port, so
	// the request is not loopback; the token only lets it call the spawn-set URL.
	if s.parentAgentToolsURL == "" || (!isLoopbackRemoteAddr(r.RemoteAddr) && !s.parentAgentToolsViaDocker()) || !parentAgentToolsSource(sess) || strings.TrimSpace(parentID) == "" {
		return nil, fmt.Errorf("parent agent tools require a direct host session")
	}
	turnCtx, cancel := context.WithCancel(ctx)
	capability := &parentAgentToolsCapability{
		ctx: turnCtx, token: token,
		endpoint: s.parentAgentToolsURL + "/mcp/sessions/" + url.PathEscape(parentID),
	}
	s.parentAgentToolsMu.Lock()
	if s.parentAgentTools == nil {
		s.parentAgentTools = make(map[string]*parentAgentToolsCapability)
	}
	s.parentAgentTools[sess.ID] = capability
	s.parentAgentToolsMu.Unlock()
	stop := context.AfterFunc(r.Context(), cancel)
	return func() {
		stop()
		cancel()
		s.parentAgentToolsMu.Lock()
		delete(s.parentAgentTools, sess.ID)
		s.parentAgentToolsMu.Unlock()
	}, nil
}

func (s *Server) parentAgentToolsViaDocker() bool {
	u, err := url.Parse(s.parentAgentToolsURL)
	return err == nil && u.Hostname() == dockerParentHost
}

func (s *Server) parentAgentToolsForSession(sess *session.Session) *parentAgentToolsCapability {
	if sess == nil || !parentAgentToolsSource(sess) {
		return nil
	}
	s.parentAgentToolsMu.Lock()
	defer s.parentAgentToolsMu.Unlock()
	capability := s.parentAgentTools[sess.ID]
	if capability == nil || capability.ctx.Err() != nil {
		return nil
	}
	return capability
}

func (s *Server) withParentAgentTools(manager *tools.Manager, sess *session.Session) *tools.Manager {
	capability := s.parentAgentToolsForSession(sess)
	if capability == nil {
		return manager
	}
	// Never mutate the shared manager, including when its fast path was used.
	manager = manager.Clone()
	// Wrap only surviving registrations so child-side tool filters remain in force.
	delegated := isDelegatedSession(sess)
	for _, def := range manager.GetDefinitions() {
		if parentAgentToolAllowedForSession(def.Name, s.parentAgentToolsViaDocker(), delegated) {
			tool, _ := manager.Get(def.Name)
			manager.Register(&parentAgentTool{Tool: tool, server: s, sessionID: sess.ID, capability: capability})
		}
	}
	// Clone preserves tool instances; compound tools must use this manager, not
	// the original manager which still has the child-local handlers.
	if _, ok := manager.Get("parallel"); ok {
		manager.Register(tools.NewParallelTool(manager))
	}
	if _, ok := manager.Get("pipeline"); ok {
		manager.Register(tools.NewPipelineTool(manager))
	}
	return manager
}

type parentAgentTool struct {
	tools.Tool
	server     *Server
	sessionID  string
	capability *parentAgentToolsCapability
}

func (t *parentAgentTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	id, _ := ctx.Value("session_id").(string)
	sess, err := t.server.sessionManager.Get(t.sessionID)
	if err != nil || id != t.sessionID || t.server.parentAgentToolsForSession(sess) != t.capability {
		return nil, fmt.Errorf("parent agent tools capability is no longer active")
	}
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]interface{}{"name": t.Name(), "arguments": params},
	})
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(t.capability.ctx, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, t.capability.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.capability.token)
	req.Header.Set("Content-Type", "application/json")
	if delegationAsync(ctx) {
		req.Header.Set(delegationAsyncHeader, "1")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		// Transport errors can contain endpoint details; never include headers.
		return nil, fmt.Errorf("parent agent tools request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("parent agent tools returned HTTP %d", resp.StatusCode)
	}
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError           bool `json:"isError"`
			StructuredContent *struct {
				ToolResult *tools.Result `json:"a2gentToolResult"`
			} `json:"structuredContent"`
		} `json:"result"`
		Error *mcpBridgeRPCError `json:"error"`
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, mcpBridgeMaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read parent agent tools response: %w", err)
	}
	if len(body) > mcpBridgeMaxBodyBytes {
		return nil, fmt.Errorf("parent agent tools response exceeds %d bytes", mcpBridgeMaxBodyBytes)
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, fmt.Errorf("invalid parent agent tools response: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("parent agent tools: %s", rpc.Error.Message)
	}
	if structured := rpc.Result.StructuredContent; structured != nil && structured.ToolResult != nil {
		if structured.ToolResult.Success == rpc.Result.IsError {
			return nil, fmt.Errorf("inconsistent parent agent tools result")
		}
		return structured.ToolResult, nil
	}
	var texts []string
	for _, content := range rpc.Result.Content {
		texts = append(texts, content.Text)
	}
	result := &tools.Result{Success: !rpc.Result.IsError, Output: strings.Join(texts, "\n")}
	if rpc.Result.IsError {
		result.Error = result.Output
	}
	return result, nil
}
