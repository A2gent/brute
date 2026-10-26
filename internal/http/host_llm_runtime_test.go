package http

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/claudecli"
)

func TestHostClaudeUsesSessionLocalClient(t *testing.T) {
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://127.0.0.1:5445/v1")
	cfg := config.DefaultConfig()
	cfg.HostClaudeCLI = true
	cfg.Providers["anthropic:work"] = config.Provider{ClaudeConfigDir: t.TempDir()}
	server := &Server{config: cfg}
	for _, ref := range []config.ProviderType{config.ProviderAnthropic, "anthropic:work"} {
		for _, sessionAware := range []bool{false, true} {
			var client any
			var err error
			if sessionAware {
				client, err = server.createBaseLLMClientForSession(ref, "sonnet", nil)
			} else {
				client, err = server.createBaseLLMClient(ref, "sonnet")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := client.(*claudecli.Client); !ok {
				t.Fatalf("provider %s: expected session-local Claude client, got %T", ref, client)
			}
		}
	}
	client, ok := server.createParentProxyLLMClient(config.ProviderOpenAI, "test")
	if !ok || reflect.TypeOf(client).String() != "*lmstudio.Client" {
		t.Fatalf("API providers must keep the parent proxy, got %T", client)
	}
}

func TestHostClaudeFallbackExpandsLocally(t *testing.T) {
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://127.0.0.1:5445/v1")
	cfg := config.DefaultConfig()
	cfg.HostClaudeCLI = true
	cfg.FallbackAggregates = []config.FallbackAggregate{{ID: "qa", Chain: []config.FallbackChainNode{{Provider: "anthropic", Model: "sonnet"}, {Provider: "anthropic", Model: "opus"}}}}
	server := &Server{config: cfg}
	nodes, err := server.fallbackNodesForProvider("fallback_chain:qa")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("host fallback nodes = %v, err = %v", nodes, err)
	}
	if client, ok := server.createParentProxyLLMClient("fallback_chain:qa", ""); ok {
		t.Fatalf("fallback must expand in child, got %T", client)
	}
	if err := server.validateProviderRefForExecution("fallback_chain:missing"); err == nil {
		t.Fatal("missing host aggregate must not be accepted as an opaque parent ref")
	}
}

func TestParentProxyEnvKeepsHostClaudeProfileAndAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "host-key")
	cfg := config.DefaultConfig()
	cfg.HostClaudeCLI = true
	cfg.Providers["anthropic:work"] = config.Provider{ClaudeConfigDir: "/profiles/work"}
	server := &Server{config: cfg}
	for _, base := range []string{"http://127.0.0.1:5445/v1", "http://[::1]:5445/v1"} {
		args := server.parentProxyEnvArgs("anthropic:work", base)
		env := map[string]string{}
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--env" {
				k, v, _ := strings.Cut(args[i+1], "=")
				env[k] = v
			}
		}
		raw := env[config.HostLLMEnvKey]
		var payload struct {
			Providers map[string]config.Provider `json:"providers"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatalf("host Claude config missing: %v", err)
		}
		if payload.Providers["anthropic:work"].ClaudeConfigDir != "/profiles/work" {
			t.Fatal("host profile lost")
		}
		if _, exists := env["ANTHROPIC_API_KEY"]; exists {
			t.Fatal("host Claude auth must be inherited, not replaced by proxy credentials")
		}
	}
	dockerArgs := strings.Join(server.parentProxyEnvArgs("anthropic", "http://host.docker.internal:5445/v1"), "\n")
	if strings.Contains(dockerArgs, config.HostLLMEnvKey+"=") || !strings.Contains(dockerArgs, "ANTHROPIC_API_KEY=a2gent-proxy") {
		t.Fatal("Docker proxy/auth behavior changed")
	}
}
