package http

import (
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestResolveContextWindowForOpenRouterOwlAlpha(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderOpenRouter)
	cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{
		Name:  string(config.ProviderOpenRouter),
		Model: "openrouter/openrouter/owl-alpha",
	}
	server := &Server{config: cfg}

	if got := server.resolveContextWindowForProvider(config.ProviderOpenRouter, "openrouter/openrouter/owl-alpha"); got != 1000000 {
		t.Fatalf("resolveContextWindowForProvider(openrouter, owl-alpha) = %d, want 1000000", got)
	}
}

func TestResolveContextWindowForProviderUsesConfiguredOverride(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{
		Name:          string(config.ProviderOpenRouter),
		Model:         "openrouter/openrouter/owl-alpha",
		ContextWindow: 64000,
	}
	server := &Server{config: cfg}

	if got := server.resolveContextWindowForProvider(config.ProviderOpenRouter, "openrouter/openrouter/owl-alpha"); got != 64000 {
		t.Fatalf("resolveContextWindowForProvider configured override = %d, want 64000", got)
	}
}
