package config

import (
	"testing"
)

func TestLoadAppliesAutoRouterEnv(t *testing.T) {
	t.Setenv("AAGENT_DATA_PATH", t.TempDir())
	t.Setenv("AAGENT_CONFIG_PATH", t.TempDir()+"/none.json")
	encoded, err := EncodeAutoRouterEnv(Provider{
		RouterProvider: "fallback_chain:cheap",
		APIKey:         "must-not-leak",
		RouterRules:    []RouterRule{{Match: "hard", Provider: "anthropic", Model: "opus", ReasoningEffort: "high"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(AutoRouterEnvKey, encoded)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers[string(ProviderAutoRouter)]
	if p.RouterProvider != "fallback_chain:cheap" || len(p.RouterRules) != 1 || p.RouterRules[0].ReasoningEffort != "high" {
		t.Fatalf("router config not applied: %+v", p)
	}
	if p.APIKey != "" {
		t.Fatalf("secrets must not be forwarded: %+v", p)
	}
}
