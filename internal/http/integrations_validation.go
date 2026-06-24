package http

// Validation and normalization helpers shared by integration CRUD handlers.

import (
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/storage"
)

func newIntegrationFromRequest(req IntegrationRequest) (*storage.Integration, error) {
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	name := strings.TrimSpace(req.Name)
	if req.Config == nil {
		req.Config = map[string]string{}
	}

	integration := &storage.Integration{
		Provider: provider,
		Name:     name,
		Mode:     mode,
		Enabled:  true,
		Config:   trimConfig(req.Config),
	}
	if integration.Provider == "telegram" {
		if integration.Config == nil {
			integration.Config = map[string]string{}
		}
		// Telegram integration now always operates in all-groups mode.
		integration.Config["allow_all_group_chats"] = "true"
		delete(integration.Config, "chat_id")
		delete(integration.Config, "project_scope")
		delete(integration.Config, "group_project_map")
	}
	if req.Enabled != nil {
		integration.Enabled = *req.Enabled
	}

	if err := validateIntegration(*integration); err != nil {
		return nil, err
	}

	if integration.Name == "" {
		integration.Name = defaultIntegrationName(integration.Provider)
	}

	return integration, nil
}

func validateIntegration(integration storage.Integration) error {
	if integration.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if _, ok := supportedIntegrationProviders[integration.Provider]; !ok {
		return fmt.Errorf("unsupported provider: %s", integration.Provider)
	}

	if integration.Mode == "" {
		return fmt.Errorf("mode is required")
	}
	if _, ok := supportedIntegrationModes[integration.Mode]; !ok {
		return fmt.Errorf("unsupported mode: %s", integration.Mode)
	}
	if integration.Provider == "webhook" && integration.Mode == "duplex" {
		return fmt.Errorf("webhook currently supports notify_only mode")
	}
	if integration.Provider == "x" && integration.Mode == "duplex" {
		return fmt.Errorf("x currently supports notify_only mode")
	}

	requiredFields := requiredConfigFields[integration.Provider]
	for _, field := range requiredFields {
		if strings.TrimSpace(integration.Config[field]) == "" {
			return fmt.Errorf("missing required config field: %s", field)
		}
	}
	if integration.Provider == "a2_registry" {
		transport := strings.TrimSpace(strings.ToLower(integration.Config["transport"]))
		if transport == "" {
			transport = "grpc"
		}
		switch transport {
		case "grpc":
			if strings.TrimSpace(integration.Config["square_grpc_addr"]) == "" {
				return fmt.Errorf("missing required config field: square_grpc_addr")
			}
		case "websocket":
			if strings.TrimSpace(integration.Config["square_ws_url"]) == "" {
				return fmt.Errorf("missing required config field: square_ws_url")
			}
		default:
			return fmt.Errorf("unsupported a2_registry transport: %s", transport)
		}
	}
	if integration.Provider == "webhook" {
		url := strings.ToLower(strings.TrimSpace(integration.Config["url"]))
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return fmt.Errorf("webhook url must start with http:// or https://")
		}
	}

	return nil
}

func trimConfig(config map[string]string) map[string]string {
	out := make(map[string]string, len(config))
	for key, value := range config {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		out[k] = strings.TrimSpace(value)
	}
	return out
}

func integrationToResponse(integration *storage.Integration) IntegrationResponse {
	configCopy := make(map[string]string, len(integration.Config))
	for key, value := range integration.Config {
		if isSensitiveIntegrationConfigKey(key) && strings.TrimSpace(value) != "" {
			configCopy[key] = "***"
			continue
		}
		configCopy[key] = value
	}

	return IntegrationResponse{
		ID:        integration.ID,
		Provider:  integration.Provider,
		Name:      integration.Name,
		Mode:      integration.Mode,
		Enabled:   integration.Enabled,
		Config:    configCopy,
		CreatedAt: integration.CreatedAt,
		UpdatedAt: integration.UpdatedAt,
	}
}

func isSensitiveIntegrationConfigKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "api_key", "api_secret", "access_token", "access_token_secret", "bot_token", "client_secret", "refresh_token":
		return true
	default:
		return false
	}
}

func defaultIntegrationName(provider string) string {
	switch provider {
	case "telegram":
		return "Telegram"
	case "slack":
		return "Slack"
	case "discord":
		return "Discord"
	case "whatsapp":
		return "WhatsApp"
	case "webhook":
		return "Webhook"
	case "x":
		return "X"
	case "google_calendar":
		return "Google Calendar"
	case "elevenlabs":
		return "ElevenLabs"
	case "perplexity":
		return "Perplexity"
	case "youtube":
		return "YouTube"
	case "brave_search":
		return "Brave Search"
	case "exa":
		return "Exa"
	case "tavily":
		return "Tavily"
	default:
		return provider
	}
}
