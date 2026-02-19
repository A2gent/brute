package http

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestMCPManageToolAddListRemove(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	tool := newMCPManageTool(&Server{store: store})
	ctx := context.Background()

	add1 := map[string]interface{}{
		"action": "add",
		"server": map[string]interface{}{
			"name":      "fetch",
			"transport": "stdio",
			"command":   "uvx",
			"args":      []string{"mcp-server-fetch"},
		},
	}
	result := mustExecuteTool(t, ctx, tool, add1)
	if !result.Success {
		t.Fatalf("expected add success, got error: %s", result.Error)
	}

	payload := decodeOutputMap(t, result.Output)
	if payload["action"] != "add" {
		t.Fatalf("expected action add, got %v", payload["action"])
	}
	if payload["created"] != true {
		t.Fatalf("expected created=true on first add, got %v", payload["created"])
	}

	server1 := mustServerPayload(t, payload)
	if enabled, _ := server1["enabled"].(bool); enabled {
		t.Fatalf("expected default enabled=false, got true")
	}

	add2 := map[string]interface{}{
		"action": "add",
		"server": map[string]interface{}{
			"name":      "fetch",
			"transport": "http",
			"url":       "http://127.0.0.1:8080/mcp",
			"enabled":   true,
		},
	}
	result = mustExecuteTool(t, ctx, tool, add2)
	if !result.Success {
		t.Fatalf("expected second add success, got error: %s", result.Error)
	}
	payload = decodeOutputMap(t, result.Output)
	if payload["created"] != false {
		t.Fatalf("expected created=false on duplicate name add, got %v", payload["created"])
	}
	server2 := mustServerPayload(t, payload)
	if enabled, _ := server2["enabled"].(bool); !enabled {
		t.Fatalf("expected enabled=true after update, got false")
	}
	if transport, _ := server2["transport"].(string); transport != "http" {
		t.Fatalf("expected transport=http after update, got %q", transport)
	}

	listReq := map[string]interface{}{"action": "list"}
	result = mustExecuteTool(t, ctx, tool, listReq)
	if !result.Success {
		t.Fatalf("expected list success, got error: %s", result.Error)
	}
	payload = decodeOutputMap(t, result.Output)
	if payload["count"] != float64(1) {
		t.Fatalf("expected count=1, got %v", payload["count"])
	}
	serversRaw, ok := payload["servers"].([]interface{})
	if !ok || len(serversRaw) != 1 {
		t.Fatalf("expected one server in list, got %T len=%d", payload["servers"], len(serversRaw))
	}

	removeReq := map[string]interface{}{
		"action": "remove",
		"server": map[string]interface{}{"name": "fetch"},
	}
	result = mustExecuteTool(t, ctx, tool, removeReq)
	if !result.Success {
		t.Fatalf("expected remove success, got error: %s", result.Error)
	}
	payload = decodeOutputMap(t, result.Output)
	if payload["removed"] != true {
		t.Fatalf("expected removed=true, got %v", payload["removed"])
	}

	result = mustExecuteTool(t, ctx, tool, listReq)
	if !result.Success {
		t.Fatalf("expected list success after remove, got error: %s", result.Error)
	}
	payload = decodeOutputMap(t, result.Output)
	if payload["count"] != float64(0) {
		t.Fatalf("expected count=0 after remove, got %v", payload["count"])
	}

	result = mustExecuteTool(t, ctx, tool, removeReq)
	if !result.Success {
		t.Fatalf("expected remove no-op success, got error: %s", result.Error)
	}
	payload = decodeOutputMap(t, result.Output)
	if payload["removed"] != false {
		t.Fatalf("expected removed=false for missing server, got %v", payload["removed"])
	}
}

func TestMCPManageToolValidation(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	tool := newMCPManageTool(&Server{store: store})
	ctx := context.Background()

	invalidAction := map[string]interface{}{"action": "enable"}
	result := mustExecuteTool(t, ctx, tool, invalidAction)
	if result.Success {
		t.Fatalf("expected invalid action to fail")
	}

	missingServer := map[string]interface{}{"action": "add"}
	result = mustExecuteTool(t, ctx, tool, missingServer)
	if result.Success {
		t.Fatalf("expected missing server to fail")
	}

	badTransport := map[string]interface{}{
		"action": "add",
		"server": map[string]interface{}{
			"name":      "bad",
			"transport": "sse",
		},
	}
	result = mustExecuteTool(t, ctx, tool, badTransport)
	if result.Success {
		t.Fatalf("expected invalid transport to fail")
	}
}

func mustExecuteTool(t *testing.T, ctx context.Context, tool *mcpManageTool, req map[string]interface{}) *toolResult {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to encode request: %v", err)
	}
	result, err := tool.Execute(ctx, payload)
	if err != nil {
		t.Fatalf("execute returned error: %v", err)
	}
	if result == nil {
		t.Fatalf("execute returned nil result")
	}
	return &toolResult{Success: result.Success, Output: result.Output, Error: result.Error}
}

type toolResult struct {
	Success bool
	Output  string
	Error   string
}

func decodeOutputMap(t *testing.T, output string) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("failed to decode output JSON: %v\noutput=%s", err, output)
	}
	return payload
}

func mustServerPayload(t *testing.T, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	server, ok := payload["server"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected server payload object, got %T", payload["server"])
	}
	return server
}
