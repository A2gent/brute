package http

import (
	"os"
	"strings"

	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/tools"
)

// registerClassifyTool keeps the classifier out of tool definitions unless a
// usable key exists. Use the manager's cwd so project and sub-agent managers
// resolve classification input files in their own workspace.
func (s *Server) registerClassifyTool(manager *tools.Manager) {
	if s == nil || manager == nil {
		return
	}
	apiKey, model, baseURL := s.resolveClassifyCredentials()
	if apiKey == "" {
		manager.Unregister("classify")
		return
	}
	manager.Register(tools.NewClassifyTool(manager.WorkDir(), jev.NewClient(apiKey, model, baseURL)))
}

// Provider configuration wins over the environment, then enabled integrations.
// Keep model/base URL paired with the selected credential source; an environment
// key uses provider model/base URL and falls back to the client's defaults.
func (s *Server) resolveClassifyCredentials() (apiKey, model, baseURL string) {
	if s.config != nil {
		provider := s.config.Providers["jev"]
		apiKey = strings.TrimSpace(provider.APIKey)
		model = strings.TrimSpace(provider.Model)
		baseURL = strings.TrimSpace(provider.BaseURL)
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY"))
	}
	if apiKey != "" {
		return apiKey, model, baseURL
	}
	if s.store != nil {
		integrations, err := s.store.ListIntegrations()
		if err != nil {
			logging.Warn("Failed to load integrations for classify tool: %v", err)
			return "", "", ""
		}
		for _, integration := range integrations {
			if integration == nil || !integration.Enabled || integration.Provider != "jev" {
				continue
			}
			key := strings.TrimSpace(integration.Config["api_key"])
			if key == "" {
				continue
			}
			return key, strings.TrimSpace(integration.Config["model"]), strings.TrimSpace(integration.Config["base_url"])
		}
	}
	return "", "", ""
}
