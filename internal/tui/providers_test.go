package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
)

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
