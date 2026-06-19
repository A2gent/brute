package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/openaicodex"
)

func TestInitLLMClientUsesOpenAICodexOAuth(t *testing.T) {
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

	client, err := initLLMClient(cfg)
	if err != nil {
		t.Fatalf("initLLMClient(openai_codex with OAuth): %v", err)
	}
	if _, ok := client.(*openaicodex.Client); !ok {
		t.Fatalf("initLLMClient(openai_codex with OAuth) returned %T, want *openaicodex.Client", client)
	}
}

func TestInitLLMClientUsesOpenAICodexAuthCache(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	writeCodexAuthCache(t, "oauth-cache-token")

	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderOpenAICodex)
	cfg.DefaultModel = "gpt-5.5"
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:  string(config.ProviderOpenAICodex),
		Model: "gpt-5.5",
	}

	client, err := initLLMClient(cfg)
	if err != nil {
		t.Fatalf("initLLMClient(openai_codex with Codex auth cache): %v", err)
	}
	if _, ok := client.(*openaicodex.Client); !ok {
		t.Fatalf("initLLMClient(openai_codex with Codex auth cache) returned %T, want *openaicodex.Client", client)
	}
	if cfg.Providers[string(config.ProviderOpenAICodex)].OAuth == nil {
		t.Fatal("expected initLLMClient to load OAuth into in-memory config")
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
