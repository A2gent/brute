package http

import "strings"

func (s *Server) a2gentRegistryAPIKey(baseURL string) string {
	if s == nil || s.store == nil {
		return ""
	}
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		return ""
	}
	target := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, integration := range integrations {
		if integration == nil || integration.Provider != "a2_registry" || !integration.Enabled {
			continue
		}
		registryURL := strings.TrimRight(strings.TrimSpace(integration.Config["registry_url"]), "/")
		if registryURL == "" {
			registryURL = defaultLocalRegistryURL
		}
		if strings.EqualFold(target, registryURL+"/v1") {
			return strings.TrimSpace(integration.Config["api_key"])
		}
	}
	return ""
}
