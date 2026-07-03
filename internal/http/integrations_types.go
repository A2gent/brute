package http

// Integration CRUD request/response DTOs and provider registry metadata.

import (
	"time"
)

var supportedIntegrationProviders = map[string]struct{}{
	"telegram":        {},
	"slack":           {},
	"discord":         {},
	"whatsapp":        {},
	"webhook":         {},
	"x":               {},
	"elevenlabs":      {},
	"google_calendar": {},
	"jira":            {},
	"circleci":        {},
	"perplexity":      {},
	"brave_search":    {},
	"exa":             {},
	"tavily":          {},
	"leonardo":        {},
	"youtube":         {},
	"a2_registry":     {},
}

var supportedIntegrationModes = map[string]struct{}{
	"notify_only": {},
	"duplex":      {},
}

var requiredConfigFields = map[string][]string{
	"telegram":        {"bot_token"},
	"slack":           {"bot_token", "channel_id"},
	"discord":         {"bot_token", "channel_id"},
	"whatsapp":        {"access_token", "phone_number_id", "recipient"},
	"webhook":         {"url"},
	"x":               {"api_key", "api_secret", "access_token", "access_token_secret"},
	"elevenlabs":      {"api_key"},
	"google_calendar": {"client_id", "client_secret", "refresh_token"},
	"jira":            {"base_url", "email", "api_token"},
	"circleci":        {"api_token"},
	"perplexity":      {"api_key"},
	"brave_search":    {"api_key"},
	"exa":             {"api_key"},
	"tavily":          {"api_key"},
	"leonardo":        {"api_key"},
	"youtube":         {},
	"a2_registry":     {"api_key"},
}

type IntegrationRequest struct {
	Provider string            `json:"provider"`
	Name     string            `json:"name"`
	Mode     string            `json:"mode"`
	Enabled  *bool             `json:"enabled,omitempty"`
	Config   map[string]string `json:"config"`
}

type IntegrationResponse struct {
	ID        string            `json:"id"`
	Provider  string            `json:"provider"`
	Name      string            `json:"name"`
	Mode      string            `json:"mode"`
	Enabled   bool              `json:"enabled"`
	Config    map[string]string `json:"config"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type IntegrationTestResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}
