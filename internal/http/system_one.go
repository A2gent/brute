package http

import (
	"strings"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/logging"
)

const (
	systemOneBackendSettingKey = "A2GENT_SYSTEM_ONE_BACKEND"
	systemOneModelSettingKey   = "A2GENT_SYSTEM_ONE_MODEL"
	systemOneBackendJev        = "jev"
	systemOneBackendLMStudio   = "lmstudio"
)

// systemOneSelection reads the backend/model chosen on the System One settings page.
// An empty model keeps the backend default (pinned Jev model or the LM Studio provider model).
func (s *Server) systemOneSelection() (backend, model string) {
	backend = systemOneBackendJev
	if s == nil || s.store == nil {
		return backend, ""
	}
	settings, err := s.store.GetSettings()
	if err != nil {
		return backend, ""
	}
	if strings.EqualFold(strings.TrimSpace(settings[systemOneBackendSettingKey]), systemOneBackendLMStudio) {
		backend = systemOneBackendLMStudio
	}
	return backend, strings.TrimSpace(settings[systemOneModelSettingKey])
}

// systemOneClient builds the classifier behind every Jev consumer (classify, relevance_gate,
// admission, toolsets, browser_act, job gates). nil means "not configured" and consumers fail open.
// egressKey is the credential the egress guard must redact; local backends have none.
func (s *Server) systemOneClient() (client *jev.Client, egressKey string) {
	backend, model := s.systemOneSelection()
	if backend == systemOneBackendLMStudio {
		if model == "" && s.config != nil {
			model = s.resolveModelForProvider(config.ProviderLMStudio)
		}
		if model == "" || s.config == nil {
			return nil, ""
		}
		chat, err := s.createBaseLLMClient(config.ProviderLMStudio, model)
		if err != nil {
			logging.Warn("System One LM Studio backend unavailable: %v", err)
			return nil, ""
		}
		return jev.NewLLMClient(chat, model), ""
	}
	apiKey, providerModel, baseURL := s.resolveClassifyCredentials()
	if apiKey == "" {
		return nil, ""
	}
	if model == "" {
		model = providerModel
	}
	return jev.NewClient(apiKey, model, baseURL), apiKey
}
