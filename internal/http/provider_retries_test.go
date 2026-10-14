package http

import (
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/providerstest"
	"testing"
)

func TestProviderFactoryRetryCounts(t *testing.T) {
	providerstest.CheckRetries(t, func(cfg *config.Config, provider config.ProviderType) (llm.Client, error) {
		return (&Server{config: cfg}).createLLMClient(provider, "", nil)
	})
}
