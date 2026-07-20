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

func TestProviderSessionPersistenceForConfig_DefaultsAnthropicTrue(t *testing.T) {
	server := &Server{}

	if got := server.providerSessionPersistenceForConfig(config.ProviderAnthropic, nil); !got {
		t.Fatalf("Anthropic default provider session persistence = false, want true")
	}
}

func TestProviderSessionPersistenceForConfig_ExplicitFalseDisablesAnthropic(t *testing.T) {
	server := &Server{}
	disabled := false

	if got := server.providerSessionPersistenceForConfig(config.ProviderAnthropic, &disabled); got {
		t.Fatalf("Anthropic explicit stateful_responses=false keeps provider session persistence enabled")
	}
}
