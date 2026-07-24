package http

import (
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
)

const promptCacheConservativeTTL = 5 * time.Minute

func sessionPromptCache(sess *session.Session) *PromptCachePayload {
	if sess == nil || sess.Metadata == nil {
		return nil
	}
	lastRequestAt, err := time.Parse(time.RFC3339Nano, sessionMetadataString(sess.Metadata, "last_llm_request_at"))
	if err != nil {
		return nil
	}
	provider := config.NormalizeProviderRef(sessionMetadataString(sess.Metadata, "last_llm_provider"))
	if provider == "" {
		return nil
	}

	estimated := false
	switch {
	case config.IsClaudeProviderRef(provider):
		// Claude's default prompt cache TTL is five minutes and refreshes on use.
	case provider == string(config.ProviderOpenAICodex):
		// OpenAI documents a 5-10 minute inactivity window, so expose only the
		// conservative lower bound and label it as an estimate.
		estimated = true
	case provider == string(config.ProviderOpenAI):
		if !supportsOpenAIPromptCache(sessionMetadataString(sess.Metadata, "last_llm_model")) {
			return nil
		}
		estimated = true
	case provider == string(config.ProviderCursor):
		// Cursor reuses the underlying model-provider cache. TTL depends on the
		// routed backend (often ~5m for Anthropic-backed models), so surface a
		// conservative five-minute estimate to incentivize timely follow-ups.
		estimated = true
	default:
		return nil
	}

	cachedTokens := int(metadataNumber(sess.Metadata, "last_llm_cached_input_tokens"))
	return &PromptCachePayload{
		Provider:          provider,
		Model:             sessionMetadataString(sess.Metadata, "last_llm_model"),
		LastRequestAt:     lastRequestAt,
		ExpiresAt:         lastRequestAt.Add(promptCacheConservativeTTL),
		TTLSeconds:        int(promptCacheConservativeTTL.Seconds()),
		CachedInputTokens: cachedTokens,
		HitObserved:       cachedTokens > 0,
		Estimated:         estimated,
	}
}

func supportsOpenAIPromptCache(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		model = model[idx+1:]
	}
	for _, prefix := range []string{"gpt-4o", "gpt-4.1", "gpt-4.5", "gpt-5", "gpt-6", "o1", "o3", "o4"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

func sessionMetadataString(metadata map[string]interface{}, key string) string {
	raw, _ := metadata[key].(string)
	return strings.TrimSpace(raw)
}
