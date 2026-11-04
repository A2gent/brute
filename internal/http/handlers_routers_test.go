package http

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/config"
)

func TestAutoRouterDockerEnvArgsUsesNamedRouter(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	server.config.Providers[string(config.ProviderAutoRouter)] = config.Provider{
		RouterProvider: "fallback_chain:cheap",
		RouterRules:    []config.RouterRule{{Match: "default", Provider: "anthropic", Model: "default-model"}},
	}
	server.config.Routers = []config.NamedRouter{{
		ID:             "coder",
		Name:           "Coder",
		RouterProvider: "fallback_chain:cheap",
		RouterRules:    []config.RouterRule{{Match: "hard", Provider: "anthropic", Model: "named-model"}},
	}}

	named := strings.Join(server.autoRouterDockerEnvArgs("automatic_router", "coder"), "\n")
	if !strings.Contains(named, "named-model") || strings.Contains(named, "default-model") {
		t.Fatalf("named router config not forwarded: %s", named)
	}
	fallback := strings.Join(server.autoRouterDockerEnvArgs("automatic_router", "missing"), "\n")
	if !strings.Contains(fallback, "default-model") {
		t.Fatalf("unknown router must fall back to default: %s", fallback)
	}
	if got := server.autoRouterDockerEnvArgs("automatic_router", ""); !strings.Contains(strings.Join(got, "\n"), "default-model") {
		t.Fatalf("empty router must use default: %v", got)
	}
}

func TestResolveLocalDockerAgentProxyLLMForcesAutoRouterWhenRouterSet(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	got := server.resolveLocalDockerAgentProxyLLM(createLocalDockerAgentRequest{LLM: localDockerAgentYAMLLLM{Provider: "anthropic", Model: "opus", Router: "coder"}})
	if got.Provider != string(config.ProviderAutoRouter) || got.Model != "" || got.Router != "coder" {
		t.Fatalf("unexpected resolved llm: %#v", got)
	}
}

func TestLLMFingerprintChangesWithRouter(t *testing.T) {
	a := &agentdef.Definition{}
	b := &agentdef.Definition{LLM: agentdef.LLM{Router: "coder"}}
	if llmFingerprint(a) == llmFingerprint(b) {
		t.Fatalf("router change must invalidate warm runtimes")
	}
}
