package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
)

func TestCreateLLMClientAutoRouterRoutesCursorWithoutAnthropicHTTPFallback(t *testing.T) {
	tmp := t.TempDir()
	fakeAgent := filepath.Join(tmp, "agent")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\",\"usage\":{\"inputTokens\":1,\"outputTokens\":1}}'\n"
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake cursor agent: %v", err)
	}
	t.Setenv("AAGENT_CURSOR_CLI_PATH", fakeAgent)
	t.Setenv("AAGENT_CURSOR_CLI_FORCE", "true")

	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderAutoRouter)
	cfg.DefaultModel = "ornith-1.0-35b"
	cfg.WorkDir = tmp
	cfg.Providers[string(config.ProviderAutoRouter)] = config.Provider{
		Name:           string(config.ProviderAutoRouter),
		RouterProvider: string(config.ProviderLMStudio),
		RouterRules: []config.RouterRule{
			{Match: "single", Provider: string(config.ProviderCursor), Model: "composer-2.5"},
		},
	}
	cfg.Providers[string(config.ProviderLMStudio)] = config.Provider{
		Name:    string(config.ProviderLMStudio),
		BaseURL: "http://127.0.0.1:1/v1",
		Model:   "ornith-1.0-35b",
	}
	cfg.Providers[string(config.ProviderCursor)] = config.Provider{
		Name:  string(config.ProviderCursor),
		Model: "composer-2.5",
	}

	m := Model{appConfig: cfg}
	client := m.createLLMClient(config.ProviderAutoRouter)
	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "add alias cc"}},
	})
	if err != nil {
		if strings.Contains(err.Error(), `Post "/messages"`) {
			t.Fatalf("automatic router used Anthropic HTTP fallback instead of Cursor CLI: %v", err)
		}
		t.Fatalf("Chat returned error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("response content = %q, want ok", resp.Content)
	}
}

func TestValidateActiveProviderConfigAcceptsOpenAICodexOAuth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderOpenAICodex)
	cfg.DefaultModel = "gpt-5.5"
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:  string(config.ProviderOpenAICodex),
		Model: "gpt-5.5",
		OAuth: &config.OAuthConfig{
			AccessToken: "oauth-token",
		},
	}

	m := Model{appConfig: cfg}
	if err := m.validateActiveProviderConfig(); err != nil {
		t.Fatalf("validateActiveProviderConfig(openai_codex with OAuth): %v", err)
	}
}

func TestValidateActiveProviderConfigAcceptsOpenAICodexAuthCache(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	writeCodexAuthCache(t, "oauth-cache-token")

	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderOpenAICodex)
	cfg.DefaultModel = "gpt-5.5"
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:  string(config.ProviderOpenAICodex),
		Model: "gpt-5.5",
	}

	m := Model{appConfig: cfg}
	if err := m.validateActiveProviderConfig(); err != nil {
		t.Fatalf("validateActiveProviderConfig(openai_codex with Codex auth cache): %v", err)
	}
	if cfg.Providers[string(config.ProviderOpenAICodex)].OAuth == nil {
		t.Fatal("expected validation to load OAuth into in-memory config")
	}
}

func TestCreateLLMClientUsesOpenAICodexOAuth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got, want := r.Header.Get("Authorization"), "Bearer oauth-token"; got != want {
			t.Errorf("Authorization header = %q, want %q", got, want)
		}
		http.Error(w, "stop after auth header check", http.StatusUnauthorized)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderOpenAICodex)
	cfg.DefaultModel = "gpt-5.5"
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:            string(config.ProviderOpenAICodex),
		BaseURL:         server.URL,
		Model:           "gpt-5.5",
		ReasoningEffort: "medium",
		OAuth: &config.OAuthConfig{
			AccessToken: "oauth-token",
		},
	}

	m := Model{appConfig: cfg}
	client := m.createLLMClient(config.ProviderOpenAICodex)
	_, err := client.Chat(context.Background(), &llm.ChatRequest{
		Model: "gpt-5.5",
		Messages: []llm.Message{
			{Role: "user", Content: "hello"},
		},
	})
	if err == nil {
		t.Fatal("expected local test server to reject the request")
	}
	if requests != 1 {
		t.Fatalf("test server received %d requests, want 1", requests)
	}
}

func TestCreateLLMClientUsesOpenAICodexAuthCache(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	writeCodexAuthCache(t, "oauth-cache-token")

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got, want := r.Header.Get("Authorization"), "Bearer oauth-cache-token"; got != want {
			t.Errorf("Authorization header = %q, want %q", got, want)
		}
		http.Error(w, "stop after auth header check", http.StatusUnauthorized)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderOpenAICodex)
	cfg.DefaultModel = "gpt-5.5"
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:    string(config.ProviderOpenAICodex),
		BaseURL: server.URL,
		Model:   "gpt-5.5",
	}

	m := Model{appConfig: cfg}
	client := m.createLLMClient(config.ProviderOpenAICodex)
	_, err := client.Chat(context.Background(), &llm.ChatRequest{
		Model: "gpt-5.5",
		Messages: []llm.Message{
			{Role: "user", Content: "hello"},
		},
	})
	if err == nil {
		t.Fatal("expected local test server to reject the request")
	}
	if requests != 1 {
		t.Fatalf("test server received %d requests, want 1", requests)
	}
	if cfg.Providers[string(config.ProviderOpenAICodex)].OAuth == nil {
		t.Fatal("expected createLLMClient to load OAuth into in-memory config")
	}
}

func writeCodexAuthCache(t *testing.T, accessToken string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	data := []byte(`{"tokens":{"access_token":` + strconv.Quote(accessToken) + `}}`)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), data, 0o600); err != nil {
		t.Fatalf("write Codex auth cache: %v", err)
	}
}
