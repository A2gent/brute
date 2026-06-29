package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type mcpListToolsTool struct {
	server *Server
}

type mcpCallTool struct {
	server *Server
}

type mcpListToolsParams struct {
	Server string `json:"server,omitempty"`
}

type mcpCallParams struct {
	Server    string                 `json:"server"`
	Tool      string                 `json:"tool"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
	Args      map[string]interface{} `json:"args,omitempty"`
}

func newMCPListToolsTool(server *Server) *mcpListToolsTool {
	return &mcpListToolsTool{server: server}
}

func (t *mcpListToolsTool) Name() string {
	return "mcp_list_tools"
}

func (t *mcpListToolsTool) Description() string {
	return "List callable tools exposed by configured MCP servers available to the current session. Use this before mcp_call when the MCP tool name or input schema is unknown."
}

func (t *mcpListToolsTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"server": map[string]interface{}{
				"type":        "string",
				"description": "Optional MCP server id or name. Omit to list tools for every enabled MCP server available to this session.",
			},
		},
	}
}

func (t *mcpListToolsTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p mcpListToolsParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}

	projectID := t.server.mcpToolContextProjectID(ctx)
	servers, err := t.server.selectMCPServersForTool(ctx, p.Server, false)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	if len(servers) == 0 {
		return &tools.Result{Success: false, Error: "no enabled MCP servers are available to this session"}, nil
	}

	items := make([]map[string]interface{}, 0, len(servers))
	for _, server := range servers {
		item := map[string]interface{}{
			"id":        server.ID,
			"name":      server.Name,
			"scope":     mcpServerScopeLabel(server, projectID),
			"transport": server.Transport,
		}
		toolsList, listErr := t.server.listMCPServerTools(ctx, server)
		if listErr != nil {
			item["error"] = listErr.Error()
		} else {
			item["tools"] = toolsList
			item["tool_count"] = len(toolsList)
		}
		items = append(items, item)
	}

	return jsonToolOutput(map[string]interface{}{
		"servers": items,
	})
}

func newMCPCallTool(server *Server) *mcpCallTool {
	return &mcpCallTool{server: server}
}

func (t *mcpCallTool) Name() string {
	return "mcp_call"
}

func (t *mcpCallTool) Description() string {
	return "Call a tool exposed by a configured MCP server available to the current session. Use mcp_list_tools first to discover server tool names and schemas."
}

func (t *mcpCallTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"server": map[string]interface{}{
				"type":        "string",
				"description": "MCP server id or name, such as the configured Atlassian/Jira server name.",
			},
			"tool": map[string]interface{}{
				"type":        "string",
				"description": "Exact MCP tool name to call, as returned by mcp_list_tools.",
			},
			"arguments": map[string]interface{}{
				"type":                 "object",
				"description":          "Arguments for the MCP tool. Match the input schema returned by mcp_list_tools.",
				"additionalProperties": true,
			},
		},
		"required": []string{"server", "tool"},
	}
}

func (t *mcpCallTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p mcpCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	toolName := strings.TrimSpace(p.Tool)
	if toolName == "" {
		return &tools.Result{Success: false, Error: "tool is required"}, nil
	}

	servers, err := t.server.selectMCPServersForTool(ctx, p.Server, true)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	if len(servers) != 1 {
		return &tools.Result{Success: false, Error: "server must identify exactly one enabled MCP server"}, nil
	}

	arguments := p.Arguments
	if arguments == nil {
		arguments = p.Args
	}
	if arguments == nil {
		arguments = map[string]interface{}{}
	}

	result, err := t.server.callMCPServerTool(ctx, servers[0], toolName, arguments)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	payload := map[string]interface{}{
		"server": map[string]interface{}{
			"id":        servers[0].ID,
			"name":      servers[0].Name,
			"scope":     mcpServerScopeLabel(servers[0], t.server.mcpToolContextProjectID(ctx)),
			"transport": servers[0].Transport,
		},
		"tool":   toolName,
		"result": result,
	}
	if text := mcpToolResultText(result); text != "" {
		payload["text"] = text
	}

	body, marshalErr := json.MarshalIndent(payload, "", "  ")
	if marshalErr != nil {
		return nil, fmt.Errorf("failed to encode MCP tool output: %w", marshalErr)
	}
	metadata := map[string]interface{}{
		"mcp_server_id":   servers[0].ID,
		"mcp_server_name": servers[0].Name,
		"mcp_tool":        toolName,
	}
	if isMCPToolResultError(result) {
		errText := strings.TrimSpace(mcpToolResultText(result))
		if errText == "" {
			errText = "MCP tool returned an error"
		}
		return &tools.Result{Success: false, Output: string(body), Error: errText, Metadata: metadata}, nil
	}
	return &tools.Result{Success: true, Output: string(body), Metadata: metadata}, nil
}

func (s *Server) mcpToolContextProjectID(ctx context.Context) string {
	if s == nil || s.sessionManager == nil || ctx == nil {
		return ""
	}
	sessionID, _ := ctx.Value("session_id").(string)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	sess, err := s.sessionManager.Get(sessionID)
	if err != nil || sess == nil {
		return ""
	}
	return sessionProjectID(sess)
}

func sessionProjectID(sess *session.Session) string {
	if sess == nil || sess.ProjectID == nil {
		return ""
	}
	return strings.TrimSpace(*sess.ProjectID)
}

func (s *Server) selectMCPServersForTool(ctx context.Context, selector string, requireSelector bool) ([]*storage.MCPServer, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("MCP server store is not available")
	}

	selector = strings.TrimSpace(selector)
	if selector == "" && requireSelector {
		return nil, fmt.Errorf("server is required")
	}

	projectID := s.mcpToolContextProjectID(ctx)
	servers, err := s.store.ListMCPServers()
	if err != nil {
		return nil, fmt.Errorf("failed to list MCP servers: %w", err)
	}
	servers = filterMCPServersForProject(servers, projectID, projectID != "")

	enabled := make([]*storage.MCPServer, 0, len(servers))
	for _, server := range servers {
		if server != nil && server.Enabled {
			enabled = append(enabled, server)
		}
	}
	if selector == "" {
		return enabled, nil
	}

	exact := make([]*storage.MCPServer, 0, 1)
	needle := strings.ToLower(selector)
	for _, server := range enabled {
		if strings.ToLower(strings.TrimSpace(server.ID)) == needle || strings.ToLower(strings.TrimSpace(server.Name)) == needle {
			exact = append(exact, server)
		}
	}
	if len(exact) > 0 {
		return exact, nil
	}

	partial := make([]*storage.MCPServer, 0, 1)
	for _, server := range enabled {
		name := strings.ToLower(strings.TrimSpace(server.Name))
		id := strings.ToLower(strings.TrimSpace(server.ID))
		if strings.Contains(name, needle) || strings.Contains(needle, name) || strings.Contains(id, needle) {
			partial = append(partial, server)
		}
	}
	if len(partial) == 1 {
		return partial, nil
	}
	if len(partial) > 1 {
		return nil, fmt.Errorf("MCP server selector %q is ambiguous; available servers: %s", selector, formatMCPServerSelectorList(enabled, projectID))
	}
	return nil, fmt.Errorf("MCP server %q is not available to this session; available servers: %s", selector, formatMCPServerSelectorList(enabled, projectID))
}

func formatMCPServerSelectorList(servers []*storage.MCPServer, projectID string) string {
	labels := make([]string, 0, len(servers))
	for _, server := range servers {
		if server == nil {
			continue
		}
		labels = append(labels, fmt.Sprintf("%s (%s)", strings.TrimSpace(server.Name), mcpServerScopeLabel(server, projectID)))
	}
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, ", ")
}

func mcpToolResultText(result map[string]interface{}) string {
	if len(result) == 0 {
		return ""
	}
	content, _ := result["content"].([]interface{})
	parts := make([]string, 0, len(content))
	for _, item := range content {
		entry := mapFromAny(item)
		if strings.TrimSpace(asString(entry["type"])) == "text" {
			if text := strings.TrimSpace(asString(entry["text"])); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	if structured := mapFromAny(result["structuredContent"]); len(structured) > 0 {
		if body, err := json.MarshalIndent(structured, "", "  "); err == nil {
			return string(body)
		}
	}
	return ""
}

func isMCPToolResultError(result map[string]interface{}) bool {
	value, _ := result["isError"].(bool)
	return value
}

var _ tools.Tool = (*mcpListToolsTool)(nil)
var _ tools.Tool = (*mcpCallTool)(nil)
