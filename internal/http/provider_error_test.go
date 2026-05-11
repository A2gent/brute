package http

import (
	"errors"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestAdaptProviderErrorMessageAddsCodexExpiredTokenHint(t *testing.T) {
	server := &Server{}
	err := server.adaptProviderErrorMessage(
		config.ProviderOpenAICodex,
		errors.New(`LLM error: OpenAI Codex error (401): {"error":{"message":"Provided authentication token is expired. Please try signing in again.","code":"token_expired"},"status":401}`),
	)

	if err == nil {
		t.Fatalf("adapted error = nil")
	}
	got := err.Error()
	if !strings.Contains(got, "Reconnect OpenAI Codex in provider settings") {
		t.Fatalf("adapted error missing reconnect hint: %s", got)
	}
	if !strings.Contains(got, "/providers/openai_codex") {
		t.Fatalf("adapted error missing provider settings path: %s", got)
	}
}

func TestAdaptProviderErrorMessageDoesNotAddCodexHintForOtherProviders(t *testing.T) {
	server := &Server{}
	err := server.adaptProviderErrorMessage(
		config.ProviderOpenAI,
		errors.New(`OpenAI error (401): {"error":{"message":"Provided authentication token is expired.","code":"token_expired"}}`),
	)

	if err == nil {
		t.Fatalf("adapted error = nil")
	}
	if strings.Contains(err.Error(), "/providers/openai_codex") {
		t.Fatalf("non-Codex error included Codex provider path: %s", err.Error())
	}
}
