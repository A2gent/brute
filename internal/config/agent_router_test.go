package config

import "testing"

func TestAgentRouterEnablesUnlistedAgents(t *testing.T) {
	r := AgentRouter{Agents: []AgentRouterAgent{{ID: "off", Enabled: false}, {ID: "on", Enabled: true}}}
	if !r.AgentEnabled("unlisted") || !r.AgentEnabled("on") || r.AgentEnabled("off") {
		t.Fatal("unexpected agent enabled flags")
	}
}

func TestEffectiveAgentRouterModelFallsBackToAutomaticRouter(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Providers[string(ProviderAutoRouter)] = Provider{RouterProvider: "jev", RouterModel: "m"}
	if p, m, _ := cfg.EffectiveAgentRouterModel(); p != "jev" || m != "m" {
		t.Fatalf("expected automatic router fallback, got %s/%s", p, m)
	}
	cfg.AgentRouter = AgentRouter{RouterProvider: "google", RouterModel: "g"}
	if p, m, _ := cfg.EffectiveAgentRouterModel(); p != "google" || m != "g" {
		t.Fatalf("expected agent router model, got %s/%s", p, m)
	}
}
