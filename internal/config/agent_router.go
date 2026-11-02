package config

import "strings"

// AgentRouter picks which configured agent handles a new session. Agents that
// are not listed are enabled, so a fresh install routes across every agent.
type AgentRouter struct {
	RouterProvider        string             `json:"router_provider,omitempty"`
	RouterModel           string             `json:"router_model,omitempty"`
	RouterReasoningEffort string             `json:"router_reasoning_effort,omitempty"`
	Agents                []AgentRouterAgent `json:"agents,omitempty"`
}

type AgentRouterAgent struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

func (r AgentRouter) AgentEnabled(id string) bool {
	id = strings.TrimSpace(id)
	for _, agent := range r.Agents {
		if strings.TrimSpace(agent.ID) == id {
			return agent.Enabled
		}
	}
	return true
}

// EffectiveAgentRouterModel falls back to the automatic router's router model so the
// agent router works without separate setup.
func (c *Config) EffectiveAgentRouterModel() (provider string, model string, effort string) {
	r := c.AgentRouter
	if NormalizeProviderRef(r.RouterProvider) != "" {
		return NormalizeProviderRef(r.RouterProvider), strings.TrimSpace(r.RouterModel), strings.TrimSpace(r.RouterReasoningEffort)
	}
	auto := c.Providers[string(ProviderAutoRouter)]
	return NormalizeProviderRef(auto.RouterProvider), strings.TrimSpace(auto.RouterModel), strings.TrimSpace(auto.RouterReasoningEffort)
}
