package http

import (
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/providerstest"
)

func TestOpenAIResponsesProvider(t *testing.T) {
	providerstest.CheckOpenAIResponses(t, func(cfg *config.Config) (llm.Client, error) {
		return (&Server{config: cfg}).createBaseLLMClient(config.ProviderOpenAI, "")
	})
}

func TestOpenAIResponsesKeepsUsageAndPromptCacheStatus(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{APIKey: "test-key", Model: "gpt-5.4-mini"}
	server := &Server{config: cfg}
	usage := server.providerUsageStatus(t.Context(), config.ProviderOpenAI)
	if usage.Provider != "openai" || usage.Source != "OpenAI API" || usage.Status != providerUsageStatusUnavailable {
		t.Fatalf("API-key usage status = %+v", usage)
	}
	sess := session.New("test")
	sess.Metadata["last_llm_request_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	sess.Metadata["last_llm_provider"] = "openai"
	sess.Metadata["last_llm_model"] = "gpt-5.4-mini"
	sess.Metadata["last_llm_cached_input_tokens"] = float64(1024)
	cache := sessionPromptCache(sess, cfg)
	if cache == nil || cache.Provider != "openai" || !cache.HitObserved || cache.CachedInputTokens != 1024 || !cache.Estimated {
		t.Fatalf("API-key cache status = %+v", cache)
	}
}
