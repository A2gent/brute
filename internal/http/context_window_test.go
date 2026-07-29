package http

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestResolveContextWindowForOpenRouterOwlAlpha(t *testing.T) {
	// Global OpenRouter context cache is shared across packages; keep serial.
	config.ResetOpenRouterModelContextCache()
	config.CacheOpenRouterModelContextWindow("owl-alpha", 1000000)
	t.Cleanup(config.ResetOpenRouterModelContextCache)

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

func TestResolveContextWindowForCustomClaudeRef(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	customRef := "anthropic:work"
	cfg.Providers[customRef] = config.Provider{
		Name:          customRef,
		ContextWindow: 123456,
	}
	server := &Server{config: cfg}

	if got := server.resolveContextWindowForProvider(config.ProviderType(customRef), "claude-sonnet-4-6"); got != 123456 {
		t.Fatalf("resolveContextWindowForProvider(custom Claude) = %d, want 123456", got)
	}
}

func TestResolveContextWindowForFallbackIncludesCustomClaudeNode(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	customRef := "anthropic:work"
	claudePath := filepath.Join(t.TempDir(), "claude-work")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Providers[customRef] = config.Provider{
		Name:          customRef,
		BinaryPath:    claudePath,
		ContextWindow: 50000,
	}
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		Name:          string(config.ProviderOpenAI),
		APIKey:        "test-key",
		ContextWindow: 100000,
	}
	cfg.Providers[string(config.ProviderFallback)] = config.Provider{
		FallbackChainNodes: []config.FallbackChainNode{
			{Provider: customRef, Model: "claude-sonnet-4-6"},
			{Provider: string(config.ProviderOpenAI), Model: "gpt-5.5"},
		},
	}
	server := &Server{config: cfg}

	if got := server.resolveContextWindowForProvider(config.ProviderFallback, ""); got != 50000 {
		t.Fatalf("resolveContextWindowForProvider(fallback with custom Claude) = %d, want 50000", got)
	}
}

func TestResolveContextWindowForFallbackUsesCustomClaudeDefault(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	customRef := "anthropic:work"
	claudePath := filepath.Join(t.TempDir(), "claude-work")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Providers[customRef] = config.Provider{Name: customRef, BinaryPath: claudePath}
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		Name:          string(config.ProviderOpenAI),
		APIKey:        "test-key",
		ContextWindow: 1000000,
	}
	cfg.Providers[string(config.ProviderFallback)] = config.Provider{
		FallbackChainNodes: []config.FallbackChainNode{
			{Provider: customRef, Model: "claude-sonnet-4-6"},
			{Provider: string(config.ProviderOpenAI), Model: "gpt-5.5"},
		},
	}
	server := &Server{config: cfg}

	want := config.GetProviderDefinition(config.ProviderAnthropic).ContextWindow
	if got := server.resolveContextWindowForProvider(config.ProviderFallback, ""); got != want {
		t.Fatalf("resolveContextWindowForProvider(fallback custom Claude default) = %d, want %d", got, want)
	}
}
