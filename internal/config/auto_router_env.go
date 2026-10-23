package config

import (
	"encoding/json"
	"os"
	"strings"
)

// AutoRouterEnvKey carries the host's automatic_router settings into Docker
// child agents. Router rules live only in the host config.json, so without this
// a child started with AAGENT_PROVIDER=automatic_router has no router_provider
// and fails validation.
const AutoRouterEnvKey = "A2GENT_AUTO_ROUTER_CONFIG"

// EncodeAutoRouterEnv serializes only the routing fields (no keys or OAuth data).
func EncodeAutoRouterEnv(p Provider) (string, error) {
	data, err := json.Marshal(Provider{
		RouterProvider: p.RouterProvider,
		RouterModel:    p.RouterModel,
		RouterRules:    p.RouterRules,
	})
	return string(data), err
}

// applyAutoRouterEnv overlays the routing fields from the environment. It runs
// after the config file is loaded so the parent's settings win in children.
func applyAutoRouterEnv(cfg *Config) {
	raw := strings.TrimSpace(os.Getenv(AutoRouterEnvKey))
	if raw == "" {
		return
	}
	var in Provider
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	key := string(ProviderAutoRouter)
	p := cfg.Providers[key]
	p.Name = key
	p.RouterProvider = in.RouterProvider
	p.RouterModel = in.RouterModel
	p.RouterRules = in.RouterRules
	cfg.Providers[key] = p
}
