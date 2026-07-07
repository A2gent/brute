package integrationtools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const (
	appSignalDefaultMCPEndpoint = "https://appsignal.com/api/mcp"
	appSignalMCPProtocolVersion = "2024-10-07"
	appSignalResponseLimit      = 8 * 1024 * 1024
)

var appSignalReadOnlyTools = map[string]struct{}{
	"get_applications":        {},
	"get_app_resources":       {},
	"get_exception_incidents": {},
	"get_incident":            {},
	"get_performance":         {},
	"get_traces":              {},
	"get_anomaly_incidents":   {},
	"get_triggers":            {},
	"get_log_lines":           {},
	"discover_metrics":        {},
	"get_metric_names":        {},
	"get_metric_tags":         {},
	"get_metrics_timeseries":  {},
	"get_metrics_list":        {},
}

// AppSignalQueryTool talks to AppSignal's official MCP endpoint but only allows
// read-only tool calls. This keeps the integration useful for incidents/logs/metrics
// investigations without letting agents mutate AppSignal state.
type AppSignalQueryTool struct {
	store  storage.Store
	client *http.Client
}

type AppSignalQueryParams struct {
	Operation       string                 `json:"operation"`
	IntegrationID   string                 `json:"integration_id,omitempty"`
	IntegrationName string                 `json:"integration_name,omitempty"`
	Tool            string                 `json:"tool,omitempty"`
	Arguments       map[string]interface{} `json:"arguments,omitempty"`
	Args            map[string]interface{} `json:"args,omitempty"`
}

type appSignalCredentials struct {
	Token    string
	Endpoint string
	Source   string
}

type appSignalMCPClient struct {
	client    *http.Client
	creds     appSignalCredentials
	sessionID string
}

func NewAppSignalQueryTool(store storage.Store) *AppSignalQueryTool {
	return &AppSignalQueryTool{
		store: store,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *AppSignalQueryTool) Name() string {
	return "appsignal_query"
}

func (t *AppSignalQueryTool) Description() string {
	return "Interact with AppSignal through its MCP API using a configured AppSignal integration. Only read-only AppSignal tools are allowed."
}

func (t *AppSignalQueryTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "Read-only operation: list_tools or call_tool",
				"enum":        []string{"list_tools", "call_tool"},
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific AppSignal integration ID to use (optional)",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific AppSignal integration name to use (optional)",
			},
			"tool": map[string]interface{}{
				"type":        "string",
				"description": "Read-only AppSignal MCP tool name to call. Required for call_tool.",
				"enum":        appSignalReadOnlyToolNames(),
			},
			"arguments": map[string]interface{}{
				"type":                 "object",
				"description":          "Arguments passed to the selected AppSignal MCP tool.",
				"additionalProperties": true,
			},
		},
		"required": []string{"operation"},
	}
}

func (t *AppSignalQueryTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p AppSignalQueryParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	operation := strings.ToLower(strings.TrimSpace(p.Operation))
	if operation == "" {
		return &tools.Result{Success: false, Error: "operation is required"}, nil
	}
	if operation == "call_tool" {
		toolName := strings.TrimSpace(p.Tool)
		if toolName == "" {
			return &tools.Result{Success: false, Error: "tool is required for call_tool"}, nil
		}
		if !isAppSignalReadOnlyTool(toolName) {
			return &tools.Result{Success: false, Error: fmt.Sprintf("appsignal tool %q is not allowed; only read-only tools can be called", toolName)}, nil
		}
	}

	creds, err := t.resolveCredentials(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	mcp := &appSignalMCPClient{client: t.client, creds: creds}
	if err := mcp.initialize(ctx); err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	switch operation {
	case "list_tools":
		payload, err := mcp.call(ctx, "tools/list", 2, map[string]interface{}{})
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		filtered := filterAppSignalReadOnlyTools(payload)
		output, err := json.MarshalIndent(map[string]interface{}{
			"source": creds.Source,
			"tools":  filtered,
		}, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("failed to encode AppSignal tools response: %w", err)
		}
		return &tools.Result{Success: true, Output: string(output)}, nil
	case "call_tool":
		toolName := strings.TrimSpace(p.Tool)
		if toolName == "" {
			return &tools.Result{Success: false, Error: "tool is required for call_tool"}, nil
		}
		if !isAppSignalReadOnlyTool(toolName) {
			return &tools.Result{Success: false, Error: fmt.Sprintf("appsignal tool %q is not allowed; only read-only tools can be called", toolName)}, nil
		}
		arguments := p.Arguments
		if arguments == nil {
			arguments = p.Args
		}
		if arguments == nil {
			arguments = map[string]interface{}{}
		}
		payload, err := mcp.call(ctx, "tools/call", 2, map[string]interface{}{
			"name":      toolName,
			"arguments": arguments,
		})
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		output, err := json.MarshalIndent(map[string]interface{}{
			"source": creds.Source,
			"tool":   toolName,
			"result": payload["result"],
		}, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("failed to encode AppSignal tool response: %w", err)
		}
		return &tools.Result{Success: true, Output: string(output)}, nil
	default:
		return &tools.Result{Success: false, Error: fmt.Sprintf("unsupported operation %q", operation)}, nil
	}
}

func (t *AppSignalQueryTool) resolveCredentials(integrationID string, integrationName string) (appSignalCredentials, error) {
	if t.store == nil {
		return appSignalCredentials{}, fmt.Errorf("appsignal integration is required; configure one in Integrations")
	}
	integration, err := t.selectIntegration(integrationID, integrationName)
	if err != nil {
		return appSignalCredentials{}, err
	}
	return appSignalCredentialsFromIntegration(integration)
}

func (t *AppSignalQueryTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item != nil && item.Provider == "appsignal" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled appsignal integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("appsignal integration with id %q not found or disabled", id)
	}

	if name := strings.ToLower(strings.TrimSpace(integrationName)); name != "" {
		var matched []*storage.Integration
		for _, item := range candidates {
			if strings.ToLower(strings.TrimSpace(item.Name)) == name {
				matched = append(matched, item)
			}
		}
		if len(matched) == 1 {
			return matched[0], nil
		}
		if len(matched) > 1 {
			return nil, fmt.Errorf("multiple appsignal integrations matched name %q; pass integration_id", integrationName)
		}
		return nil, fmt.Errorf("appsignal integration named %q not found", integrationName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple appsignal integrations are enabled; pass integration_id or integration_name")
}

func appSignalCredentialsFromIntegration(integration *storage.Integration) (appSignalCredentials, error) {
	if integration == nil {
		return appSignalCredentials{}, fmt.Errorf("integration is required")
	}
	creds := appSignalCredentials{
		Token:    strings.TrimSpace(integration.Config["api_key"]),
		Endpoint: strings.TrimRight(strings.TrimSpace(integration.Config["mcp_url"]), "/"),
		Source:   strings.TrimSpace(integration.Name),
	}
	if creds.Endpoint == "" {
		creds.Endpoint = appSignalDefaultMCPEndpoint
	}
	if creds.Source == "" {
		creds.Source = integration.ID
	}
	if creds.Token == "" {
		return appSignalCredentials{}, fmt.Errorf("selected appsignal integration is missing api_key")
	}
	parsed, err := url.Parse(creds.Endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return appSignalCredentials{}, fmt.Errorf("appsignal mcp_url must be an absolute URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return appSignalCredentials{}, fmt.Errorf("appsignal mcp_url must use http or https")
	}
	return creds, nil
}

func (c *appSignalMCPClient) initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", 1, map[string]interface{}{
		"protocolVersion": appSignalMCPProtocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "aagent-appsignal",
			"version": "1.0.0",
		},
	})
	if err != nil {
		return err
	}
	// Some MCP HTTP servers return no body for notifications. Ignore failures here
	// because tools/list and tools/call will fail explicitly if initialization was not accepted.
	_, _ = c.callNotification(ctx, "notifications/initialized", map[string]interface{}{})
	return nil
}

func (c *appSignalMCPClient) callNotification(ctx context.Context, method string, params interface{}) (map[string]interface{}, error) {
	return c.request(ctx, method, nil, params, false)
}

func (c *appSignalMCPClient) call(ctx context.Context, method string, id interface{}, params interface{}) (map[string]interface{}, error) {
	return c.request(ctx, method, id, params, true)
}

func (c *appSignalMCPClient) request(ctx context.Context, method string, id interface{}, params interface{}, expectResponse bool) (map[string]interface{}, error) {
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if id != nil {
		payload["id"] = id
	}
	if params != nil {
		payload["params"] = params
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.creds.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create AppSignal MCP request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.creds.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", appSignalMCPProtocolVersion)
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("AppSignal MCP request failed: %w", err)
	}
	defer resp.Body.Close()
	if sessionID := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); sessionID != "" {
		c.sessionID = sessionID
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, appSignalResponseLimit))
	if err != nil {
		return nil, fmt.Errorf("failed to read AppSignal MCP response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("AppSignal MCP request %q failed with status %d: %s", method, resp.StatusCode, msg)
	}
	if !expectResponse && strings.TrimSpace(string(respBody)) == "" {
		return map[string]interface{}{}, nil
	}
	if strings.TrimSpace(string(respBody)) == "" {
		return nil, fmt.Errorf("AppSignal MCP response for %q was empty", method)
	}

	out, err := decodeAppSignalMCPResponse(respBody)
	if err != nil {
		return nil, fmt.Errorf("failed to decode AppSignal MCP response for %q: %w", method, err)
	}
	if rpcErr, ok := out["error"].(map[string]interface{}); ok && len(rpcErr) > 0 {
		return nil, fmt.Errorf("AppSignal MCP error for %q: %v", method, rpcErr)
	}
	return out, nil
}

func decodeAppSignalMCPResponse(body []byte) (map[string]interface{}, error) {
	trimmed := bytes.TrimSpace(body)
	if bytes.HasPrefix(trimmed, []byte("{")) {
		var out map[string]interface{}
		if err := json.Unmarshal(trimmed, &out); err != nil {
			return nil, err
		}
		return out, nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" || !strings.HasPrefix(data, "{") {
			continue
		}
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(data), &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no JSON-RPC payload found")
}

func isAppSignalReadOnlyTool(toolName string) bool {
	_, ok := appSignalReadOnlyTools[strings.TrimSpace(toolName)]
	return ok
}

func appSignalReadOnlyToolNames() []string {
	names := make([]string, 0, len(appSignalReadOnlyTools))
	for name := range appSignalReadOnlyTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func filterAppSignalReadOnlyTools(payload map[string]interface{}) []interface{} {
	result, _ := payload["result"].(map[string]interface{})
	rawTools, _ := result["tools"].([]interface{})
	filtered := make([]interface{}, 0, len(rawTools))
	for _, raw := range rawTools {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		if isAppSignalReadOnlyTool(name) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
