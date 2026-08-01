package http

import (
	"context"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
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

func TestCreateLLMClientUsesParentProxyForFallbackAggregate(t *testing.T) {
	const providerRef = "fallback_chain:mid-tier"

	proxy := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if got, want := r.URL.Path, "/v1/providers/"+providerRef+"/chat/completions"; got != want {
			t.Errorf("proxy request path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer proxy.Close()

	t.Setenv("A2GENT_PARENT_PROXY_URL", proxy.URL+"/v1")
	t.Setenv("A2GENT_PARENT_PROXY_KEY", "")

	cfg := config.DefaultConfig()
	cfg.FallbackAggregates = nil
	server := &Server{config: cfg}

	client, err := server.createLLMClient(config.ProviderType(providerRef), "", nil)
	if err != nil {
		t.Fatalf("createLLMClient returned error: %v", err)
	}
	if clientType := reflect.TypeOf(client).String(); clientType != "*lmstudio.Client" {
		t.Fatalf("expected fallback aggregate to use parent proxy client, got %s", clientType)
	}

	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("proxy client Chat returned error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("proxy client response = %q, want %q", resp.Content, "ok")
	}
}

func TestValidateProviderRefForExecutionAcceptsFallbackAggregateThroughParentProxy(t *testing.T) {
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://host.docker.internal:5445/v1")

	cfg := config.DefaultConfig()
	cfg.FallbackAggregates = nil
	server := &Server{config: cfg}

	if err := server.validateProviderRefForExecution("fallback_chain:mid-tier"); err != nil {
		t.Fatalf("validateProviderRefForExecution returned error: %v", err)
	}
	if !server.providerRefExists("fallback_chain:mid-tier") {
		t.Fatal("expected fallback aggregate ref to exist through parent proxy")
	}
	if !server.providerConfiguredForUse(config.ProviderType("fallback_chain:mid-tier")) {
		t.Fatal("expected fallback aggregate ref to be configured through parent proxy")
	}
}

func TestFallbackNodesForProviderAcceptsOpaqueAggregateThroughParentProxy(t *testing.T) {
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://host.docker.internal:5445/v1")

	cfg := config.DefaultConfig()
	cfg.FallbackAggregates = nil
	server := &Server{config: cfg}

	nodes, err := server.fallbackNodesForProvider(config.ProviderType("fallback_chain:mid-tier"))
	if err != nil {
		t.Fatalf("fallbackNodesForProvider returned error: %v", err)
	}
	if nodes != nil {
		t.Fatalf("fallbackNodesForProvider returned local nodes for opaque proxy aggregate: %#v", nodes)
	}
}

func TestFallbackAggregateStillRequiresLocalDefinitionWithoutParentProxy(t *testing.T) {
	t.Setenv("A2GENT_PARENT_PROXY_URL", "")

	cfg := config.DefaultConfig()
	cfg.FallbackAggregates = nil
	server := &Server{config: cfg}

	if _, err := server.createLLMClient(config.ProviderType("fallback_chain:mid-tier"), "", nil); err == nil {
		t.Fatal("expected missing local fallback aggregate to fail without parent proxy")
	}
	if err := server.validateProviderRefForExecution("fallback_chain:mid-tier"); err == nil {
		t.Fatal("expected missing local fallback aggregate validation to fail without parent proxy")
	}
}
