package main

import (
	"testing"

	"github.com/A2gent/brute/internal/llm/providerstest"
)

func TestOpenAIResponsesProvider(t *testing.T) {
	providerstest.CheckOpenAIResponses(t, initLLMClient)
}
