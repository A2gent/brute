package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/tools"
	"github.com/google/uuid"
)

type mcpManageTool struct {
	server *Server
}

type mcpManageParams struct {
	Action    string `json:"action"`
	ProjectID string `json:"project_id,omitempty"`
	Server    *struct {
		ProjectID      string            `json:"project_id,omitempty"`
		Name           string            `json:"name"`
		Transport      string            `json:"transport"`
		Enabled        *bool             `json:"enabled,omitempty"`
		Command        string            `json:"command,omitempty"`
		Args           []string          `json:"args,omitempty"`
		Env            map[string]string `json:"env,omitempty"`
		Cwd            string            `json:"cwd,omitempty"`
		URL            string            `json:"url,omitempty"`
		Headers        map[string]string `json:"headers,omitempty"`
		TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	} `json:"server,omitempty"`
}

func newMCPManageTool(server *Server) *mcpManageTool {
	return &mcpManageTool{server: server}
}

func (t *mcpManageTool) Name() string {
	return "mcp_manage"
}

func (t *mcpManageTool) Description() string {
	return `Manage MCP server configurations for the agent.
Actions:
- add: add or update an MCP server by name
- list: list configured MCP servers
- remove: remove an MCP server by name`
}

func (t *mcpManageTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Operation to perform",
				"enum":        []string{"add", "list", "remove"},
			},
			"project_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional project ID. When set, list returns global plus project servers; add/remove target that project scope. Omit for global scope.",
			},
			"server": map[string]interface{}{
				"type":        "object",
				"description": "Server payload for action=add and target for action=remove.",
				"properties": map[string]interface{}{
					"project_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional project ID for this server. Overrides top-level project_id. Omit for global scope.",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Required for action=add/remove. MCP server name.",
					},
					"transport": map[string]interface{}{
						"type":        "string",
						"description": "Required for action=add. One of: stdio, http.",
						"enum":        []string{mcpTransportStdio, mcpTransportHTTP},
					},
					"enabled": map[string]interface{}{
						"type":        "boolean",
						"description": "Optional for action=add. Defaults to false.",
					},
					"command": map[string]interface{}{
						"type":        "string",
						"description": "For action=add with transport=stdio: command executable path/name.",
					},
					"args": map[string]interface{}{
						"type":        "array",
						"description": "Optional command arguments for stdio transport.",
						"items":       map[string]interface{}{"type": "string"},
					},
					"env": map[string]interface{}{
						"type":                 "object",
						"description":          "Optional environment variables for stdio transport.",
						"additionalProperties": map[string]interface{}{"type": "string"},
					},
					"cwd": map[string]interface{}{
						"type":        "string",
						"description": "Optional working directory for stdio transport.",
					},
					"url": map[string]interface{}{
						"type":        "string",
						"description": "For action=add with transport=http: server URL.",
					},
					"headers": map[string]interface{}{
						"type":                 "object",
						"description":          "Optional HTTP headers for http transport.",
						"additionalProperties": map[string]interface{}{"type": "string"},
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "Optional test timeout seconds (1-120). Defaults to 60.",
					},
				},
			},
		},
		"required": []string{"action"},
	}
}

func (t *mcpManageTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	_ = ctx

	var p mcpManageParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	switch strings.ToLower(strings.TrimSpace(p.Action)) {
	case "add":
		return t.handleAdd(p)
	case "list":
		return t.handleList(p)
	case "remove":
		return t.handleRemove(p)
	default:
		return &tools.Result{Success: false, Error: "invalid action; expected one of: add, list, remove"}, nil
	}
}

func (t *mcpManageTool) handleAdd(p mcpManageParams) (*tools.Result, error) {
	if p.Server == nil {
		return &tools.Result{Success: false, Error: "server is required for action=add"}, nil
	}

	enabled := false
	if p.Server.Enabled != nil {
		enabled = *p.Server.Enabled
	}

	req := MCPServerRequest{
		Name:           p.Server.Name,
		Transport:      p.Server.Transport,
		Enabled:        &enabled,
		Command:        p.Server.Command,
		Args:           p.Server.Args,
		Env:            p.Server.Env,
		Cwd:            p.Server.Cwd,
		URL:            p.Server.URL,
		Headers:        p.Server.Headers,
		TimeoutSeconds: p.Server.TimeoutSeconds,
	}
	if projectID := p.serverProjectID(); projectID != "" {
		req.ProjectID = &projectID
	}

	next, err := newMCPServerFromRequest(req)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	if err := t.server.validateMCPServerProject(next.ProjectID); err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	servers, err := t.server.store.ListMCPServers()
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to list MCP servers: " + err.Error()}, nil
	}

	now := time.Now()
	created := true
	for _, existing := range servers {
		if existing == nil {
			continue
		}
		if mcpServerProjectID(existing) == mcpServerProjectID(next) && strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(next.Name)) {
			created = false
			next.ID = existing.ID
			next.ProjectID = existing.ProjectID
			next.CreatedAt = existing.CreatedAt
			next.LastTestAt = existing.LastTestAt
			next.LastTestSuccess = existing.LastTestSuccess
			next.LastTestMessage = existing.LastTestMessage
			next.LastEstimatedTokens = existing.LastEstimatedTokens
			next.LastToolCount = existing.LastToolCount
			break
		}
	}
	if created {
		next.ID = uuid.New().String()
		next.CreatedAt = now
	}
	next.UpdatedAt = now

	if err := t.server.store.SaveMCPServer(next); err != nil {
		return &tools.Result{Success: false, Error: "failed to save MCP server: " + err.Error()}, nil
	}

	payload := map[string]interface{}{
		"action":  "add",
		"created": created,
		"server":  mcpServerToResponse(next),
	}
	return jsonToolOutput(payload)
}

func (t *mcpManageTool) handleList(p mcpManageParams) (*tools.Result, error) {
	servers, err := t.server.store.ListMCPServers()
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to list MCP servers: " + err.Error()}, nil
	}
	projectID := strings.TrimSpace(p.ProjectID)
	if projectID != "" {
		if _, err := t.server.store.GetProject(projectID); err != nil {
			return &tools.Result{Success: false, Error: "project not found: " + err.Error()}, nil
		}
	}
	servers = filterMCPServersForProject(servers, projectID, projectID != "")

	resp := make([]MCPServerResponse, 0, len(servers))
	for _, server := range servers {
		if server == nil {
			continue
		}
		resp = append(resp, mcpServerToResponse(server))
	}

	payload := map[string]interface{}{
		"action":  "list",
		"count":   len(resp),
		"servers": resp,
	}
	return jsonToolOutput(payload)
}

func (t *mcpManageTool) handleRemove(p mcpManageParams) (*tools.Result, error) {
	if p.Server == nil {
		return &tools.Result{Success: false, Error: "server is required for action=remove"}, nil
	}
	name := strings.TrimSpace(p.Server.Name)
	if name == "" {
		return &tools.Result{Success: false, Error: "server.name is required for action=remove"}, nil
	}
	projectID := strings.TrimSpace(p.serverProjectID())
	if projectID != "" {
		if _, err := t.server.store.GetProject(projectID); err != nil {
			return &tools.Result{Success: false, Error: "project not found: " + err.Error()}, nil
		}
	}

	servers, err := t.server.store.ListMCPServers()
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to list MCP servers: " + err.Error()}, nil
	}

	removed := false
	for _, existing := range servers {
		if existing == nil {
			continue
		}
		if mcpServerProjectID(existing) == projectID && strings.EqualFold(strings.TrimSpace(existing.Name), name) {
			if err := t.server.store.DeleteMCPServer(existing.ID); err != nil {
				return &tools.Result{Success: false, Error: "failed to delete MCP server: " + err.Error()}, nil
			}
			removed = true
			break
		}
	}

	payload := map[string]interface{}{
		"action":  "remove",
		"removed": removed,
		"name":    name,
	}
	return jsonToolOutput(payload)
}

func (p mcpManageParams) serverProjectID() string {
	if p.Server != nil && strings.TrimSpace(p.Server.ProjectID) != "" {
		return strings.TrimSpace(p.Server.ProjectID)
	}
	return strings.TrimSpace(p.ProjectID)
}

var _ tools.Tool = (*mcpManageTool)(nil)
