package http

import (
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
)

func TestSessionPromptCacheForAnthropicUsesFiveMinuteTTL(t *testing.T) {
	lastRequestAt := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	sess := session.New("build")
	sess.Metadata["last_llm_request_at"] = lastRequestAt.Format(time.RFC3339)
	sess.Metadata["last_llm_provider"] = "anthropic:work"
	sess.Metadata["last_llm_model"] = "claude-sonnet-4-5"
	sess.Metadata["last_llm_input_tokens"] = float64(4000)
	sess.Metadata["last_llm_cached_input_tokens"] = float64(3000)

	cache := sessionPromptCache(sess)
	if cache == nil {
		t.Fatal("expected prompt cache metadata")
	}
	if cache.Provider != "anthropic:work" || cache.Model != "claude-sonnet-4-5" {
		t.Fatalf("cache target = %q/%q", cache.Provider, cache.Model)
	}
	if cache.TTLSeconds != 300 || !cache.ExpiresAt.Equal(lastRequestAt.Add(5*time.Minute)) {
		t.Fatalf("cache expiry = %s ttl=%d", cache.ExpiresAt, cache.TTLSeconds)
	}
	if cache.Estimated {
		t.Fatal("Anthropic's configured five-minute TTL should not be marked estimated")
	}
	if !cache.HitObserved || cache.CachedInputTokens != 3000 {
		t.Fatalf("observed cache = %#v", cache)
	}
}

func TestSessionPromptCacheForOpenAIIsConservativeEstimate(t *testing.T) {
	lastRequestAt := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	sess := session.New("build")
	sess.Metadata["last_llm_request_at"] = lastRequestAt.Format(time.RFC3339Nano)
	sess.Metadata["last_llm_provider"] = string(config.ProviderOpenAICodex)
	sess.Metadata["last_llm_model"] = "gpt-5.5"

	cache := sessionPromptCache(sess)
	if cache == nil {
		t.Fatal("expected OpenAI prompt cache metadata")
	}
	if cache.TTLSeconds != 300 || !cache.Estimated {
		t.Fatalf("OpenAI cache policy = %#v", cache)
	}
}

func TestSessionPromptCacheOmitsUnsupportedProviderAndInvalidTimestamp(t *testing.T) {
	for name, metadata := range map[string]map[string]interface{}{
		"unsupported": {
			"last_llm_request_at": time.Now().UTC().Format(time.RFC3339),
			"last_llm_provider":   string(config.ProviderKimi),
		},
		"invalid timestamp": {
			"last_llm_request_at": "not-a-time",
			"last_llm_provider":   string(config.ProviderOpenAI),
		},
		"unsupported OpenAI model": {
			"last_llm_request_at": time.Now().UTC().Format(time.RFC3339),
			"last_llm_provider":   string(config.ProviderOpenAI),
			"last_llm_model":      "gpt-3.5-turbo",
		},
	} {
		t.Run(name, func(t *testing.T) {
			sess := session.New("build")
			sess.Metadata = metadata
			if got := sessionPromptCache(sess); got != nil {
				t.Fatalf("prompt cache = %#v, want nil", got)
			}
		})
	}
}

func TestSessionToResponseIncludesPromptCache(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess := session.New("build")
	sess.Metadata["last_llm_request_at"] = "2026-07-23T12:00:00Z"
	sess.Metadata["last_llm_provider"] = string(config.ProviderOpenAICodex)

	resp := server.sessionToResponse(sess)
	if resp.PromptCache == nil || resp.PromptCache.TTLSeconds != 300 {
		t.Fatalf("prompt cache response = %#v", resp.PromptCache)
	}
}
