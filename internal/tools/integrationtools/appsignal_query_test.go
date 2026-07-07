package integrationtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestAppSignalQueryToolListToolsFiltersReadOnlyTools(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		switch payload["method"] {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"appsignal"}}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"get_applications"},{"name":"update_incidents"},{"name":"get_log_lines"}]}}`))
		default:
			t.Fatalf("unexpected method: %v", payload["method"])
		}
	}))
	defer server.Close()

	store := newAppSignalTestStore(t, server.URL)
	tool := NewAppSignalQueryTool(store)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"list_tools"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %q", result.Error)
	}
	if gotAuth != "Bearer mcp-secret" {
		t.Fatalf("expected bearer auth header, got %q", gotAuth)
	}
	if !strings.Contains(result.Output, "get_applications") || !strings.Contains(result.Output, "get_log_lines") {
		t.Fatalf("expected read-only tools in output, got %s", result.Output)
	}
	if strings.Contains(result.Output, "update_incidents") {
		t.Fatalf("did not expect write tool in output, got %s", result.Output)
	}
}

func TestAppSignalQueryToolRejectsWriteTool(t *testing.T) {
	store := newAppSignalTestStore(t, "https://appsignal.example.test/api/mcp")
	tool := NewAppSignalQueryTool(store)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"call_tool","tool":"update_incidents"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "not allowed") {
		t.Fatalf("expected read-only validation error, got %#v", result)
	}
}

func TestAppSignalQueryToolCallsReadOnlyTool(t *testing.T) {
	var gotTool string
	var gotArgument string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		switch payload["method"] {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"appsignal"}}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			params := payload["params"].(map[string]interface{})
			gotTool, _ = params["name"].(string)
			args := params["arguments"].(map[string]interface{})
			gotArgument, _ = args["environment"].(string)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"[]"}]}}`))
		default:
			t.Fatalf("unexpected method: %v", payload["method"])
		}
	}))
	defer server.Close()

	store := newAppSignalTestStore(t, server.URL)
	tool := NewAppSignalQueryTool(store)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"operation":"call_tool","tool":"get_exception_incidents","arguments":{"environment":"production"}}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %q", result.Error)
	}
	if gotTool != "get_exception_incidents" || gotArgument != "production" {
		t.Fatalf("unexpected tool call: tool=%q environment=%q", gotTool, gotArgument)
	}
}

func newAppSignalTestStore(t *testing.T, endpoint string) storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	if err := store.SaveIntegration(&storage.Integration{
		ID:        "appsignal-1",
		Provider:  "appsignal",
		Name:      "AppSignal",
		Mode:      "notify_only",
		Enabled:   true,
		Config:    map[string]string{"api_key": "mcp-secret", "mcp_url": endpoint},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("failed to save integration: %v", err)
	}
	return store
}
