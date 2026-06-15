package http

import (
	"reflect"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestCreateBaseLLMClientForSessionUsesParentProxyForAnthropic(t *testing.T) {
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://host.docker.internal:5445/v1")
	t.Setenv("A2GENT_PARENT_PROXY_KEY", "")
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "/definitely/not/used")

	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderAnthropic)
	cfg.DefaultModel = "claude-sonnet-4-6"
	server := &Server{config: cfg}

	client, err := server.createBaseLLMClientForSession(config.ProviderAnthropic, "claude-sonnet-4-6", nil)
	if err != nil {
		t.Fatalf("createBaseLLMClientForSession returned error: %v", err)
	}

	clientType := reflect.TypeOf(client).String()
	if clientType != "*lmstudio.Client" {
		t.Fatalf("expected Anthropic in Docker child to use parent OpenAI-compatible proxy client, got %s", clientType)
	}
}

func TestProviderConfiguredForUseAcceptsAnthropicThroughParentProxy(t *testing.T) {
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://host.docker.internal:5445/v1")
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "/definitely/not/used")

	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderAnthropic)
	server := &Server{config: cfg}

	if !server.providerConfiguredForUse(config.ProviderAnthropic) {
		t.Fatalf("expected Anthropic to be configured inside Docker child via parent proxy")
	}
}
