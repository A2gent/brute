package http

// Integration CRUD request/response DTOs and provider registry metadata.

import (
	"time"
)

var supportedIntegrationProviders = map[string]struct{}{
	"telegram":        {},
	"telegram_user":   {},
	"slack":           {},
	"discord":         {},
	"whatsapp":        {},
	"webhook":         {},
	"x":               {},
	"elevenlabs":      {},
	"google_calendar": {},
	"gmail":           {},
	"jira":            {},
	"jev":             {},
	"circleci":        {},
	"bitbucket":       {},
	"appsignal":       {},
	"sentry":          {},
	"cloudflare":      {},
	"perplexity":      {},
	"brave_search":    {},
	"exa":             {},
	"tavily":          {},
	"leonardo":        {},
	"openai_images":   {},
	"comfyui":         {},
	"blender":         {},
	"meshy":           {},
	"tripo":           {},
	"youtube":         {},
	"a2_registry":     {},
}

var supportedIntegrationModes = map[string]struct{}{
	"notify_only": {},
	"duplex":      {},
}

var requiredConfigFields = map[string][]string{
	"telegram":        {"bot_token"},
	"telegram_user":   {"api_id", "api_hash"},
	"slack":           {"bot_token", "channel_id"},
	"discord":         {"bot_token", "channel_id"},
	"whatsapp":        {"access_token", "phone_number_id", "recipient"},
	"webhook":         {"url"},
	"x":               {"api_key", "api_secret", "access_token", "access_token_secret"},
	"elevenlabs":      {"api_key"},
	"google_calendar": {"client_id", "client_secret"},
	"gmail":           {"client_id", "client_secret"},
	"jira":            {"base_url", "email", "api_token"},
	"jev":             {"api_key"},
	"appsignal":       {"api_key"},
	"sentry":          {"api_token", "organization"},
	"cloudflare":      {"api_token"},
	"circleci":        {"api_token"},
	"bitbucket":       {"email", "api_token"},
	"perplexity":      {"api_key"},
	"brave_search":    {"api_key"},
	"exa":             {"api_key"},
	"tavily":          {"api_key"},
	"leonardo":        {"api_key"},
	"openai_images":   {"api_key"},
	"comfyui":         {"base_url"},
	"blender":         {"binary_path"},
	"meshy":           {"api_key"},
	"tripo":           {"api_key"},
	"youtube":         {},
	"a2_registry":     {"api_key"},
}

type IntegrationRequest struct {
	Provider string            `json:"provider"`
	Name     string            `json:"name"`
	Mode     string            `json:"mode"`
	Enabled  *bool             `json:"enabled,omitempty"`
	Config   map[string]string `json:"config"`
	// ProjectID binds the integration to a project: nil keeps the stored value
	// on update (global on create), "" makes it global.
	ProjectID *string `json:"project_id,omitempty"`
}

type IntegrationResponse struct {
	ID        string            `json:"id"`
	Provider  string            `json:"provider"`
	Name      string            `json:"name"`
	Mode      string            `json:"mode"`
	Enabled   bool              `json:"enabled"`
	Config    map[string]string `json:"config"`
	ProjectID string            `json:"project_id"` // "" = global
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type IntegrationTestResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}
