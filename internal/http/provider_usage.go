package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/go-chi/chi/v5"
)

const (
	providerUsageStatusUnavailable   = "unavailable"
	providerUsageStatusUnsupported   = "unsupported"
	providerUsageStatusNotConfigured = "not_configured"
)

func (s *Server) handleProviderUsage(w http.ResponseWriter, r *http.Request) {
	providerType := config.ProviderType(config.NormalizeProviderRef(chi.URLParam(r, "providerType")))
	if config.GetProviderDefinition(providerType) == nil {
		s.errorResponse(w, http.StatusNotFound, "Unknown provider")
		return
	}

	usage := s.providerUsageStatus(providerType)
	s.jsonResponse(w, http.StatusOK, usage)
}

func (s *Server) providerUsageStatus(providerType config.ProviderType) ProviderUsageResponse {
	response := ProviderUsageResponse{
		Provider:    string(providerType),
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
		Refreshable: true,
	}

	switch providerType {
	case config.ProviderOpenAI:
		response.Source = "OpenAI API"
		if !s.providerConfiguredForUse(providerType) {
			response.Status = providerUsageStatusNotConfigured
			response.UsageLeftText = "Usage left unavailable — configure an OpenAI API key first."
			return response
		}
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable — OpenAI does not expose remaining quota for this API key. Check the OpenAI dashboard; Brute will surface quota or billing errors when requests fail."
		return response
	case config.ProviderOpenAICodex:
		response.Source = "OpenAI Codex OAuth"
		if !s.providerConfiguredForUse(providerType) {
			response.Status = providerUsageStatusNotConfigured
			response.UsageLeftText = "Usage left unavailable — connect OpenAI Codex OAuth or add an API key first."
			return response
		}
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable — the ChatGPT Codex backend does not expose remaining plan or quota via Brute. Check ChatGPT/OpenAI account limits; Brute will surface quota or plan errors when requests fail."
		return response
	case config.ProviderAnthropic:
		response.Source = "Claude Code CLI"
		if !s.providerConfiguredForUse(providerType) {
			response.Status = providerUsageStatusNotConfigured
			response.UsageLeftText = "Usage left unavailable — Claude Code CLI is not available. Install Claude Code or set AAGENT_CLAUDE_CLI_PATH."
			return response
		}
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable — Claude Code CLI does not expose remaining Anthropic plan or quota. It reports per-run usage/cost only; Brute will surface rate-limit, credits, billing, or budget errors when requests fail."
		return response
	default:
		response.Status = providerUsageStatusUnsupported
		response.Source = strings.TrimSpace(string(providerType))
		response.UsageLeftText = "Usage left is not supported for this provider yet."
		response.Refreshable = false
		return response
	}
}
