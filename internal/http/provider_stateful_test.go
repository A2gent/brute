package http

import (
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestProviderStatefulResponsesForConfig_DisablesOpenAICodex(t *testing.T) {
	server := &Server{}
	enabled := true

	if got := server.providerStatefulResponsesForConfig(config.ProviderOpenAICodex, nil); got {
		t.Fatalf("OpenAI Codex default stateful responses = true, want false")
	}
	if got := server.providerStatefulResponsesForConfig(config.ProviderOpenAICodex, &enabled); got {
		t.Fatalf("OpenAI Codex configured stateful responses = true, want false")
	}
}
