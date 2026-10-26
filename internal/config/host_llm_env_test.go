package config

import (
	"os"
	"strings"
	"testing"
)

func hostLLMTestEnv(t *testing.T) string {
	data := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AAGENT_DATA_PATH", data)
	t.Setenv("AAGENT_CONFIG_PATH", t.TempDir()+"/none.json")
	return data
}

func TestHostLLMEnvRoundtrip(t *testing.T) {
	data := hostLLMTestEnv(t)
	parent := &Config{
		DataPath: "/parent/data",
		Providers: map[string]Provider{
			"anthropic:work": {Name: "work", Model: "opus", APIKey: "leak-key", BinaryPath: "/bin/claude",
				ClaudeConfigDir: "/cc", EnvOverrides: map[string]string{"X": "1"}, SensitiveSecrets: map[string]string{"TOK": "s3"},
				OAuth: &OAuthConfig{AccessToken: "leak-oauth"}},
			"fallback_chain": {Name: "fb", APIKey: "leak-key2", FallbackChain: []string{"anthropic:work"},
				FallbackChainNodes: []FallbackChainNode{{Provider: "anthropic:work", Model: "opus"}}},
			"openai": {Name: "openai", APIKey: "leak-openai"},
		},
		FallbackAggregates: []FallbackAggregate{{ID: "cheap", Chain: []FallbackChainNode{{Provider: "anthropic:work", Model: "sonnet"}}}},
	}
	enc, err := EncodeHostLLMEnv(parent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "leak-") || strings.Contains(enc, "openai") || strings.Contains(enc, "/parent/data") {
		t.Fatalf("unrelated data forwarded: %s", enc)
	}
	t.Setenv("OPENAI_API_KEY", "child-key")
	t.Setenv(HostLLMEnvKey, enc)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HostClaudeCLI || os.Getenv(HostLLMEnvKey) != "" {
		t.Fatal("host Claude mode must survive without exporting the secret payload to subprocesses")
	}
	w := cfg.Providers["anthropic:work"]
	if w.BinaryPath != "/bin/claude" || w.ClaudeConfigDir != "/cc" || w.EnvOverrides["X"] != "1" || w.SensitiveSecrets["TOK"] != "s3" || w.APIKey != "" {
		t.Fatalf("claude profile not restored: %+v", w)
	}
	if fb := cfg.Providers["fallback_chain"]; len(fb.FallbackChainNodes) != 1 || fb.FallbackChain[0] != "anthropic:work" {
		t.Fatalf("fallback not restored: %+v", fb)
	}
	if len(cfg.FallbackAggregates) != 1 || cfg.FallbackAggregates[0].ID != "cheap" {
		t.Fatalf("aggregates not restored: %+v", cfg.FallbackAggregates)
	}
	if cfg.DataPath != data {
		t.Fatalf("child data path altered: %s", cfg.DataPath)
	}
}

func TestHostLLMEnvEncodeDefaultsAndInvalid(t *testing.T) {
	enc, err := EncodeHostLLMEnv(&Config{})
	if err != nil || !strings.Contains(enc, `"anthropic"`) {
		t.Fatalf("default anthropic entry missing: %s %v", enc, err)
	}
	hostLLMTestEnv(t)
	t.Setenv(HostLLMEnvKey, `{"providers": secret-not-json`)
	_, err = Load()
	if err == nil || strings.Contains(err.Error(), "secret-not-json") {
		t.Fatalf("want error without payload, got %v", err)
	}
}
