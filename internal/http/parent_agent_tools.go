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
)

// This marker lives only in the parent's token context, never in RPC arguments.
type parentAgentToolsScopeKey struct{}
type parentAgentToolsWorkDirKey struct{}

func parentAgentToolAllowed(name string) bool {
	switch name {
	case "list_agents", "delegate_to_agent", "delegate_to_subagent":
		return true
	}
	return integrationtools.IsIntegrationBackedTool(name)
}

// Only a spawn-provisioned loopback origin is trusted; requests cannot supply a
// callback URL or credentials in it. Do not resolve DNS or follow redirects.
func trustedParentAgentToolsURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return ""
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Port() == "" {
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
	if runtime != agentdef.RuntimeHost || isDelegatedSession(sess) {
		return nil, func() {}, nil
	}
	// Resolve from the parent's stored definition, never a child header, RPC
	// argument or docker_workspace metadata supplied by a caller. Match the
	// host spawn path: configured project root, or inherited process cwd.
	workDir, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(metadataString(sess.Metadata["unified_agent_id"])) != "" {
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
	workDir, err = filepath.Abs(workDir)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(workDir)
	if err != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("parent agent tools workspace is unavailable")
	}
	scoped := context.WithValue(ctx, parentAgentToolsScopeKey{}, true)
	scoped = context.WithValue(scoped, parentAgentToolsWorkDirKey{}, workDir)
	token, revoke, err := s.mcpBridge.mintWithContext(scoped, sess.ID)
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
	if s.parentAgentToolsURL == "" || !isLoopbackRemoteAddr(r.RemoteAddr) || sess.Metadata["source"] != "direct_agent_session" || isDelegatedSession(sess) || strings.TrimSpace(parentID) == "" {
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

func (s *Server) parentAgentToolsForSession(sess *session.Session) *parentAgentToolsCapability {
	if sess == nil || sess.Metadata["source"] != "direct_agent_session" || isDelegatedSession(sess) {
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
	for _, def := range manager.GetDefinitions() {
		if parentAgentToolAllowed(def.Name) {
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
