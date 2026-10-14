package http

import (
	"os"
	"strings"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/contextcompress"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

func (s *Server) registerWebRelevanceTools(manager *tools.Manager) {
	if s == nil || manager == nil || s.config == nil {
		return
	}
	policy := s.config.Tools.WebRelevance
	options := integrationtools.WebRelevanceOptions{Enabled: policy.Enabled, Threshold: policy.Threshold, TopN: policy.TopN, MinConfidence: policy.MinConfidence, MaxPageBytes: policy.MaxPageBytes}
	var client *jev.Client
	if policy.Enabled {
		key, model, baseURL := s.resolveClassifyCredentials()
		if key != "" {
			client = jev.NewClient(key, model, baseURL)
		}
		secrets, ok := s.jevEgressSecrets(key)
		if !ok {
			client = nil
		}
		options.Secrets = secrets
	}
	for _, name := range []string{"fetch_url", "tavily_search", "exa_search", "brave_search_query"} {
		if source, ok := manager.Get(name); ok {
			manager.Register(integrationtools.NewWebRelevanceTool(source, client, options))
		}
	}
}

// jevEgressSecrets lists configured credential values for the local egress guard.
// Only values are used, never serialized into classifier state. ok is false when
// integrations cannot be listed: an incomplete list must disable Jev egress.
func (s *Server) jevEgressSecrets(jevKey string) ([]string, bool) {
	var secrets []string
	for _, provider := range s.config.Providers {
		secrets = append(secrets, provider.APIKey)
		if provider.OAuth != nil {
			secrets = append(secrets, provider.OAuth.AccessToken, provider.OAuth.RefreshToken)
		}
		for _, value := range provider.SensitiveSecrets {
			secrets = append(secrets, value)
		}
		for name, value := range provider.EnvOverrides {
			if webCredentialName(name) {
				secrets = append(secrets, value)
			}
		}
	}
	secrets = append(secrets, jevKey)
	ok := true
	if s.store != nil {
		if integrations, err := s.store.ListIntegrations(); err == nil {
			for _, integration := range integrations {
				if integration != nil {
					for name, value := range integration.Config {
						if webCredentialName(name) {
							secrets = append(secrets, value)
						}
					}
				}
			}
		} else {
			ok = false
		}
	}
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if found && webCredentialName(name) {
			secrets = append(secrets, value)
		}
	}
	return secrets, ok
}

// relevanceGate returns nil (gating off) unless tools.jev_admission is enabled
// and a Jev credential resolves, so a missing key fails open silently.
func (s *Server) relevanceGate() *contextcompress.RelevanceGate {
	if s == nil || s.config == nil || !s.jevAutomationEnabled(jevAdmissionSettingKey, s.config.Tools.JevAdmission.Enabled) {
		return nil
	}
	key, model, baseURL := s.resolveClassifyCredentials()
	if key == "" {
		return nil
	}
	secrets, ok := s.jevEgressSecrets(key)
	if !ok {
		return nil
	}
	policy := s.config.Tools.JevAdmission
	return &contextcompress.RelevanceGate{Client: jev.NewClient(key, model, baseURL), MinTokens: policy.MinTokens, Threshold: policy.Threshold, MinConfidence: policy.MinConfidence, Secrets: secrets}
}

// toolSelector mirrors relevanceGate: nil (all tools) unless tools.jev_toolsets
// is enabled and a Jev credential resolves.
func (s *Server) toolSelector() *agent.ToolSelector {
	if s == nil || s.config == nil || !s.jevAutomationEnabled(jevToolsetsSettingKey, s.config.Tools.JevToolsets.Enabled) {
		return nil
	}
	key, model, baseURL := s.resolveClassifyCredentials()
	if key == "" {
		return nil
	}
	secrets, ok := s.jevEgressSecrets(key)
	if !ok {
		return nil
	}
	return &agent.ToolSelector{Client: jev.NewClient(key, model, baseURL), Secrets: secrets, Threshold: s.config.Tools.JevToolsets.Threshold}
}

func webCredentialName(name string) bool {
	name = strings.ToLower(name)
	for _, part := range []string{"key", "token", "secret", "password", "credential", "authorization", "dsn", "database_url"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}
