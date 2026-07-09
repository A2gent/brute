package http

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

type MCPServersImportRequest struct {
	ProjectID  *string         `json:"project_id,omitempty"`
	MCPServers json.RawMessage `json:"mcpServers,omitempty"`
	MCP        json.RawMessage `json:"mcp,omitempty"`
}

type MCPServersImportResponse struct {
	Created int                 `json:"created"`
	Updated int                 `json:"updated"`
	Servers []MCPServerResponse `json:"servers"`
}

type standardMCPServerConfig struct {
	Type           string            `json:"type,omitempty"`
	Command        json.RawMessage   `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Enabled        *bool             `json:"enabled,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Timeout        int               `json:"timeout,omitempty"`
}

func parseMCPServersImportRequest(req MCPServersImportRequest) ([]MCPServerRequest, error) {
	raw := req.MCPServers
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		raw = req.MCP
	}
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, fmt.Errorf("mcpServers or mcp object is required")
	}

	var entries map[string]standardMCPServerConfig
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("invalid MCP server config JSON: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("mcpServers must contain at least one server")
	}

	out := make([]MCPServerRequest, 0, len(entries))
	for name, cfg := range entries {
		req, err := mcpServerRequestFromStandardConfig(name, cfg)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, nil
}

func mcpServerRequestFromStandardConfig(name string, cfg standardMCPServerConfig) (MCPServerRequest, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return MCPServerRequest{}, fmt.Errorf("mcp server name is required")
	}

	command, commandArgs, err := parseStandardMCPCommand(cfg.Command)
	if err != nil {
		return MCPServerRequest{}, fmt.Errorf("%s: %w", trimmedName, err)
	}

	env := cfg.Env
	if len(env) == 0 {
		env = cfg.Environment
	}

	transport, err := inferMCPTransport(cfg.Type, cfg.URL, command)
	if err != nil {
		return MCPServerRequest{}, fmt.Errorf("%s: %w", trimmedName, err)
	}

	args := append([]string{}, commandArgs...)
	args = append(args, cfg.Args...)

	enabled := true
	if cfg.Enabled != nil {
		enabled = *cfg.Enabled
	}

	return MCPServerRequest{
		Name:           trimmedName,
		Transport:      transport,
		Enabled:        &enabled,
		Command:        command,
		Args:           args,
		Env:            env,
		Cwd:            cfg.Cwd,
		URL:            cfg.URL,
		Headers:        cfg.Headers,
		TimeoutSeconds: normalizeStandardMCPTimeout(cfg.TimeoutSeconds, cfg.Timeout),
	}, nil
}

func parseStandardMCPCommand(raw json.RawMessage) (string, []string, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return "", nil, nil
	}

	var commandString string
	if err := json.Unmarshal(raw, &commandString); err == nil {
		parts := compactStrings(strings.Fields(commandString))
		if len(parts) == 0 {
			return "", nil, nil
		}
		return parts[0], parts[1:], nil
	}

	var commandParts []string
	if err := json.Unmarshal(raw, &commandParts); err != nil {
		return "", nil, fmt.Errorf("command must be a string or string array")
	}
	commandParts = compactStrings(commandParts)
	if len(commandParts) == 0 {
		return "", nil, nil
	}
	return commandParts[0], commandParts[1:], nil
}

func inferMCPTransport(rawType string, url string, command string) (string, error) {
	typ := strings.ToLower(strings.TrimSpace(rawType))
	switch typ {
	case "", "stdio", "local":
		if strings.TrimSpace(url) != "" {
			return mcpTransportHTTP, nil
		}
		return mcpTransportStdio, nil
	case "http", "streamable-http", "remote", "web", "sse":
		return mcpTransportHTTP, nil
	default:
		if strings.TrimSpace(command) != "" {
			return mcpTransportStdio, nil
		}
		return "", fmt.Errorf("unsupported MCP server type %q", rawType)
	}
}

func normalizeStandardMCPTimeout(timeoutSeconds int, timeout int) int {
	if timeoutSeconds > 0 {
		return timeoutSeconds
	}
	if timeout <= 0 {
		return 0
	}
	// OpenCode's `timeout` is milliseconds. Small copied configs from other
	// clients sometimes use seconds, so only convert values that clearly look ms.
	if timeout > mcpMaxTestTimeoutSeconds {
		return int(math.Ceil(float64(timeout) / 1000.0))
	}
	return timeout
}

func (s *Server) importMCPServers(req MCPServersImportRequest) (*MCPServersImportResponse, error) {
	requests, err := parseMCPServersImportRequest(req)
	if err != nil {
		return nil, err
	}

	projectID := normalizeMCPServerProjectID(req.ProjectID)
	if err := s.validateMCPServerProject(projectID); err != nil {
		return nil, err
	}

	servers, err := s.store.ListMCPServers()
	if err != nil {
		return nil, fmt.Errorf("failed to list MCP servers: %w", err)
	}

	resp := &MCPServersImportResponse{Servers: make([]MCPServerResponse, 0, len(requests))}
	now := time.Now()
	for _, request := range requests {
		// Scope is applied outside individual entries so pasted config can be reused
		// unchanged between global and project pages.
		request.ProjectID = projectID
		next, err := newMCPServerFromRequest(request)
		if err != nil {
			return nil, err
		}

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
			resp.Created++
		} else {
			resp.Updated++
		}
		next.UpdatedAt = now

		if err := s.store.SaveMCPServer(next); err != nil {
			return nil, fmt.Errorf("failed to save MCP server %q: %w", next.Name, err)
		}
		servers = append(servers, next)
		resp.Servers = append(resp.Servers, mcpServerToResponse(next))
	}
	return resp, nil
}

func mcpServersConfigForResponses(servers []MCPServerResponse) map[string]interface{} {
	out := make(map[string]interface{}, len(servers))
	for _, server := range servers {
		entry := map[string]interface{}{
			"enabled": server.Enabled,
		}
		if server.Transport == mcpTransportHTTP {
			entry["type"] = "streamable-http"
			entry["url"] = server.URL
			if len(server.Headers) > 0 {
				entry["headers"] = server.Headers
			}
		} else {
			entry["type"] = "stdio"
			entry["command"] = append([]string{server.Command}, server.Args...)
			if len(server.Env) > 0 {
				entry["env"] = server.Env
			}
			if strings.TrimSpace(server.Cwd) != "" {
				entry["cwd"] = server.Cwd
			}
		}
		if server.TimeoutSeconds != mcpDefaultTestTimeoutSeconds {
			entry["timeout_seconds"] = server.TimeoutSeconds
		}
		out[server.Name] = entry
	}
	return out
}
