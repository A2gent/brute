package config

import (
	"net/url"
	"regexp"
	"strings"
)

const fallbackAggregatePrefix = "fallback_chain:"

var providerTokenRe = regexp.MustCompile(`[^a-z0-9_-]+`)

// NormalizeProviderRef normalizes provider references used across config/API.
func NormalizeProviderRef(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if decoded, err := url.PathUnescape(trimmed); err == nil {
		trimmed = decoded
	}
	return strings.ToLower(strings.TrimSpace(trimmed))
}

// IsFallbackAggregateRef returns true if the provider reference points to a named fallback aggregate.
func IsFallbackAggregateRef(ref string) bool {
	return strings.HasPrefix(NormalizeProviderRef(ref), fallbackAggregatePrefix)
}

// FallbackAggregateIDFromRef extracts aggregate ID from provider ref.
func FallbackAggregateIDFromRef(ref string) string {
	normalized := NormalizeProviderRef(ref)
	if !strings.HasPrefix(normalized, fallbackAggregatePrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(normalized, fallbackAggregatePrefix))
}

// FallbackAggregateRefFromID builds a provider ref for a fallback aggregate ID.
func FallbackAggregateRefFromID(id string) string {
	normalizedID := NormalizeToken(id)
	if normalizedID == "" {
		return ""
	}
	return fallbackAggregatePrefix + normalizedID
}

// NormalizeToken normalizes user-provided IDs/tokens into lowercase kebab-safe form.
func NormalizeToken(raw string) string {
	token := NormalizeProviderRef(raw)
	token = providerTokenRe.ReplaceAllString(token, "-")
	token = strings.Trim(token, "-")
	return token
}
