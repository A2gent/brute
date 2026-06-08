package scheduler

import (
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestCreateBaseLLMClientAcceptsOpenAICodexOAuth(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderOpenAICodex)
	cfg.DefaultModel = "gpt-5.5"
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:    string(config.ProviderOpenAICodex),
		BaseURL: "https://chatgpt.com/backend-api/codex",
		Model:   "gpt-5.5",
		OAuth: &config.OAuthConfig{
			AccessToken: "oauth-token",
		},
	}

	s := &Scheduler{config: cfg}
	if _, err := s.createBaseLLMClient(config.ProviderOpenAICodex, "", "."); err != nil {
		t.Fatalf("createBaseLLMClient(openai_codex with OAuth): %v", err)
	}
}
