package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// HostLLMEnvKey carries the host's Claude profiles and fallback chains into a
// host-runtime child so it can run Claude directly instead of via the parent's
// OpenAI proxy (which loses session MCP). The payload holds Claude CLI auth
// settings by design; never log its value.
const HostLLMEnvKey = "A2GENT_HOST_LLM_CONFIG"

type hostLLMPayload struct {
	Providers          map[string]Provider `json:"providers"`
	FallbackAggregates []FallbackAggregate `json:"fallback_aggregates,omitempty"`
}

// EncodeHostLLMEnv serializes only Claude providers (CLI settings, no generic
// API keys/OAuth) and fallback chains. The default anthropic entry is always present.
func EncodeHostLLMEnv(cfg *Config) (string, error) {
	out := hostLLMPayload{Providers: map[string]Provider{}}
	if cfg != nil {
		out.FallbackAggregates = cfg.FallbackAggregates
		for ref, p := range cfg.Providers {
			switch {
			case IsClaudeProviderRef(ref):
				out.Providers[ref] = Provider{
					Name: p.Name, Model: p.Model, ContextWindow: p.ContextWindow,
					StatefulResponses: p.StatefulResponses, MinBalancePercent: p.MinBalancePercent,
					BinaryPath: p.BinaryPath, ClaudeConfigDir: p.ClaudeConfigDir, HomePath: p.HomePath,
					EnvOverrides: p.EnvOverrides, SensitiveSecrets: p.SensitiveSecrets,
				}
			case ref == string(ProviderFallback):
				out.Providers[ref] = Provider{Name: p.Name, FallbackChain: p.FallbackChain, FallbackChainNodes: p.FallbackChainNodes}
			}
		}
	}
	if _, ok := out.Providers[string(ProviderAnthropic)]; !ok {
		out.Providers[string(ProviderAnthropic)] = Provider{Name: string(ProviderAnthropic)}
	}
	data, err := json.Marshal(out)
	return string(data), err
}

// applyHostLLMEnv overlays the parent's Claude/fallback providers onto cfg.
// Errors never include the payload, which may contain secrets.
func applyHostLLMEnv(cfg *Config) error {
	raw := strings.TrimSpace(os.Getenv(HostLLMEnvKey))
	if raw == "" {
		return nil
	}
	var in hostLLMPayload
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return fmt.Errorf("invalid %s: %w", HostLLMEnvKey, err)
	}
	// Do not pass all profiles' secrets to CLI/Bash subprocesses or grandchildren.
	if err := os.Unsetenv(HostLLMEnvKey); err != nil {
		return err
	}
	cfg.HostClaudeCLI = true
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	for ref, p := range in.Providers {
		if !IsClaudeProviderRef(ref) && ref != string(ProviderFallback) {
			continue
		}
		cfg.Providers[ref] = p
	}
	cfg.FallbackAggregates = in.FallbackAggregates
	return nil
}
