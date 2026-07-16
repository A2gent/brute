package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestListProvidersMarksCursorConfiguredWhenCLIAvailable(t *testing.T) {
	tmp := t.TempDir()
	fakeAgent := filepath.Join(tmp, "agent")
	if err := os.WriteFile(fakeAgent, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("failed to write fake Cursor CLI: %v", err)
	}
	t.Setenv("AAGENT_CURSOR_CLI_PATH", fakeAgent)

	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderCursor)] = config.Provider{
		Name:   string(config.ProviderCursor),
		APIKey: "cursor-test-token",
		Model:  "composer-2.5",
	}
	server := &Server{config: cfg}

	req := httptest.NewRequest(http.MethodGet, "/providers/", nil)
	rec := httptest.NewRecorder()
	server.handleListProviders(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleListProviders status = %d, body: %s", rec.Code, rec.Body.String())
	}

	var providers []ProviderConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &providers); err != nil {
		t.Fatalf("failed to decode provider response: %v", err)
	}

	var cursor ProviderConfigResponse
	for _, provider := range providers {
		if provider.Type == string(config.ProviderCursor) {
			cursor = provider
			break
		}
	}
	if cursor.Type == "" {
		t.Fatal("provider response missing cursor provider")
	}
	if !cursor.Configured {
		t.Fatalf("expected cursor to be configured when CLI exists: %+v", cursor)
	}
	if !cursor.HasAPIKey {
		t.Fatalf("expected cursor to report stored API key: %+v", cursor)
	}
	if cursor.BaseURL != "" {
		t.Fatalf("expected cursor base URL to remain empty, got %q", cursor.BaseURL)
	}
}

func TestCursorModelsRouteReturnsAccountModels(t *testing.T) {
	tmp := t.TempDir()
	fakeAgent := filepath.Join(tmp, "agent")
	script := "#!/bin/sh\n" +
		"test \"$1\" = \"--list-models\" || exit 21\n" +
		"test \"$CURSOR_API_KEY\" = \"cursor-test-token\" || exit 22\n" +
		"printf '%s\\n' 'Available models' '' 'auto - Auto (default)' 'cursor-grok-4.5-high - Cursor Grok 4.5' 'composer-2.5 - Composer 2.5 (current)'\n"
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake Cursor CLI: %v", err)
	}
	t.Setenv("AAGENT_CURSOR_CLI_PATH", fakeAgent)

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderCursor)] = config.Provider{
		Name:   string(config.ProviderCursor),
		APIKey: "cursor-test-token",
		Model:  "composer-2.5",
	}
	server := NewServer(cfg, nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)

	req := httptest.NewRequest(http.MethodGet, "/providers/cursor/models", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("cursor models status = %d, body: %s", rec.Code, rec.Body.String())
	}

	var response ListProviderModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode cursor models response: %v", err)
	}

	expected := []string{"auto", "cursor-grok-4.5-high", "composer-2.5"}
	if len(response.Models) != len(expected) {
		t.Fatalf("cursor models len = %d, want %d (%v)", len(response.Models), len(expected), response.Models)
	}
	for index, model := range expected {
		if response.Models[index] != model {
			t.Fatalf("cursor model[%d] = %q, want %q", index, response.Models[index], model)
		}
	}
}

func TestCursorModelsRouteFallsBackWhenCLIQueryFails(t *testing.T) {
	tmp := t.TempDir()
	fakeAgent := filepath.Join(tmp, "agent")
	if err := os.WriteFile(fakeAgent, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("failed to write fake Cursor CLI: %v", err)
	}
	t.Setenv("AAGENT_CURSOR_CLI_PATH", fakeAgent)

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	cfg := config.DefaultConfig()
	server := NewServer(cfg, nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)
	req := httptest.NewRequest(http.MethodGet, "/providers/cursor/models", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("cursor models status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var response ListProviderModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode cursor models response: %v", err)
	}
	expected := []string{"composer-2.5", "composer-latest", "auto"}
	if len(response.Models) != len(expected) {
		t.Fatalf("cursor fallback models = %v, want %v", response.Models, expected)
	}
	for index, model := range expected {
		if response.Models[index] != model {
			t.Fatalf("cursor fallback model[%d] = %q, want %q", index, response.Models[index], model)
		}
	}
}
