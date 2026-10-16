package config

import (
	"strings"

	"github.com/A2gent/brute/internal/llm/anthropic"
	"github.com/A2gent/brute/internal/llm/lmstudio"
)

// ResolveContextWindow returns the effective context window for a provider/model pair.
// User-configured provider context_window wins; otherwise we use known model-specific
// limits before falling back to the provider default.
func ResolveContextWindow(providerType ProviderType, provider Provider, model string) int {
	return resolveContextWindow(GetProviderDefinition(providerType), providerType, provider, model)
}

// ResolveContextWindowForRef resolves context window for built-in and custom provider refs
// (e.g. anthropic:work) using the base provider definition when needed.
func ResolveContextWindowForRef(ref string, provider Provider, model string) int {
	normalized := NormalizeProviderRef(ref)
	return resolveContextWindow(GetProviderDefinitionForRef(normalized), ProviderType(normalized), provider, model)
}

func resolveContextWindow(def *ProviderDefinition, providerType ProviderType, provider Provider, model string) int {
	if provider.ContextWindow > 0 {
		return provider.ContextWindow
	}
	baseType := providerType
	if def != nil {
		baseType = def.Type
	}
	if baseType == ProviderLMStudio {
		if window := lmstudio.ContextWindow(provider.BaseURL, provider.APIKey, model); window > 0 {
			return window
		}
	}
	if window := modelContextWindow(baseType, model); window > 0 {
		return window
	}
	if def != nil && def.ContextWindow > 0 {
		return def.ContextWindow
	}
	return 0
}

func modelContextWindow(providerType ProviderType, model string) int {
	normalizedModel := strings.ToLower(strings.TrimSpace(model))
	if normalizedModel == "" {
		return 0
	}

	switch providerType {
	case ProviderAnthropic:
		// Anthropic's Models API is authoritative when available; the static model
		// rules below keep session context accurate before that catalog is fetched.
		if window := anthropicCachedContextWindow(normalizedModel); window > 0 {
			return window
		}
		if window := anthropic.ContextWindow(normalizedModel); window > 0 {
			return window
		}
		// Provider default is 1M (for the default Opus model); older explicit Claude
		// models without a 1M window must not inherit it.
		if strings.HasPrefix(normalizedModel, "claude-") {
			return 200000
		}
	case ProviderOpenAICodex:
		// Filled from the Codex OAuth /models context_window field.
		if window := codexCachedContextWindow(normalizedModel); window > 0 {
			return window
		}
	case ProviderOpenRouter:
		// Cache is filled from OpenRouter /api/v1/models context_length.
		// Candidate matching inside the cache handles openrouter/ prefix variants.
		if window := openRouterCachedContextWindow(normalizedModel); window > 0 {
			return window
		}
	}

	return 0
}
