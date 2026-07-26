package config

import "strings"

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
	case ProviderOpenRouter:
		// Check the dynamically-populated cache first. The cache is filled
		// from the OpenRouter /api/v1/models endpoint and contains the real
		// context_length for each model.
		if window := openRouterCachedContextWindow(model); window > 0 {
			return window
		}
		// Normalise stored id variations so the cache lookup also matches ids
		// written with or without the "openrouter/" prefix.
		stripped := strings.TrimPrefix(normalizedModel, "openrouter/")
		if window := openRouterCachedContextWindow(stripped); window > 0 {
			return window
		}
	}

	return 0
}
