package http

import (
	"os"
	"strings"

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
		// Only credential values are used by the local egress guard, never serialized
		// into classifier state. Integration IDs, settings and raw params stay local.
		for _, provider := range s.config.Providers {
			options.Secrets = append(options.Secrets, provider.APIKey)
			if provider.OAuth != nil {
				options.Secrets = append(options.Secrets, provider.OAuth.AccessToken, provider.OAuth.RefreshToken)
			}
			for _, value := range provider.SensitiveSecrets {
				options.Secrets = append(options.Secrets, value)
			}
			for name, value := range provider.EnvOverrides {
				if webCredentialName(name) {
					options.Secrets = append(options.Secrets, value)
				}
			}
		}
		options.Secrets = append(options.Secrets, key)
		if s.store != nil {
			if integrations, err := s.store.ListIntegrations(); err == nil {
				for _, integration := range integrations {
					if integration != nil {
						for name, value := range integration.Config {
							if webCredentialName(name) {
								options.Secrets = append(options.Secrets, value)
							}
						}
					}
				}
			} else {
				client = nil
			}
		}
		for _, entry := range os.Environ() {
			name, value, ok := strings.Cut(entry, "=")
			if ok && webCredentialName(name) {
				options.Secrets = append(options.Secrets, value)
			}
		}
	}
	for _, name := range []string{"fetch_url", "tavily_search", "exa_search", "brave_search_query"} {
		if source, ok := manager.Get(name); ok {
			manager.Register(integrationtools.NewWebRelevanceTool(source, client, options))
		}
	}
}

func webCredentialName(name string) bool {
	name = strings.ToLower(name)
	for _, part := range []string{"key", "token", "secret", "password", "credential", "authorization"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}
