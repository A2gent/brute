package scheduler

import (
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/providerstest"
)

func TestOpenAIResponsesProvider(t *testing.T) {
	providerstest.CheckOpenAIResponses(t, func(cfg *config.Config) (llm.Client, error) {
		return (&Scheduler{config: cfg}).createBaseLLMClient(config.ProviderOpenAI, "", "")
	})
}
