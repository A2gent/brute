package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestAppSignalDraftConnectionTestUsesUnsavedCredentials(t *testing.T) {
	var gotAuth string
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode MCP request: %v", err)
		}
		switch payload["method"] {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"appsignal"}}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
		default:
			t.Fatalf("unexpected MCP method: %v", payload["method"])
		}
	}))
	defer mcpServer.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create SQLite store: %v", err)
	}
	defer store.Close()
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)

	body, err := json.Marshal(IntegrationRequest{
		Provider: "appsignal",
		Name:     "AppSignal draft",
		Mode:     "notify_only",
		Config:   map[string]string{"api_key": "draft-token", "mcp_url": mcpServer.URL},
	})
	if err != nil {
		t.Fatalf("failed to encode test request: %v", err)
	}
	rec := httptest.NewRecorder()
	server.handleTestIntegrationDraft(rec, httptest.NewRequest(http.MethodPost, "/integrations/test", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response IntegrationTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode test response: %v", err)
	}
	if !response.Success {
		t.Fatalf("expected successful AppSignal access test, got %q", response.Message)
	}
	if gotAuth != "Bearer draft-token" {
		t.Fatalf("expected request to use draft token, got %q", gotAuth)
	}
}
