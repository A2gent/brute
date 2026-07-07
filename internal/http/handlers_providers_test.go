package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/A2gent/brute/internal/config"
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
