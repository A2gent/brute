package http

import (
	"strings"

	"github.com/A2gent/brute/internal/config"
)

// auxiliaryReasoningEffort avoids inheriting expensive provider defaults for
// one-shot work. Effort support is model-specific, not just version-specific:
// https://developers.openai.com/api/docs/guides/reasoning#reasoning-effort
func auxiliaryReasoningEffort(provider config.ProviderType, model string) string {
	if provider != config.ProviderOpenAI {
		return ""
	}
	model = strings.ToLower(strings.TrimSpace(model))
	isModel := func(base string) bool {
		return model == base || strings.HasPrefix(model, base+"-")
	}
	switch {
	case strings.Contains(model, "-chat"):
		return ""
	case isModel("gpt-6-astra"), isModel("gpt-6.1-sol"):
		return "low"
	case isModel("gpt-5.1-codex-mini"):
		return "medium"
	case isModel("gpt-5-pro"):
		return "high"
	case strings.HasPrefix(model, "gpt-5.") && strings.Contains(model, "-pro"):
		return "medium"
	case isModel("gpt-5-codex"), isModel("gpt-5.1-codex"), isModel("gpt-5.2-codex"), isModel("gpt-5.3-codex"):
		return "low"
	case strings.HasPrefix(model, "gpt-5."), isModel("gpt-6"), strings.HasPrefix(model, "gpt-6."):
		return "none"
	case isModel("gpt-5"):
		return "minimal"
	default:
		return ""
	}
}

func auxiliaryBackgroundServiceTier(provider config.ProviderType) string {
	if provider == config.ProviderOpenAI {
		return "flex"
	}
	return ""
}
