package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/go-chi/chi/v5"
)

const (
	providerUsageStatusAvailable     = "available"
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

	usage := s.providerUsageStatus(r.Context(), providerType)
	s.jsonResponse(w, http.StatusOK, usage)
}

func (s *Server) providerUsageStatus(ctx context.Context, providerType config.ProviderType) ProviderUsageResponse {
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
		usage, err := s.openAICodexUsageStatus(ctx)
		if err != nil {
			response.Source = "OpenAI Codex OAuth (ChatGPT usage)"
			response.Status = providerUsageStatusUnavailable
			response.UsageLeftText = "Usage left unavailable — failed to fetch ChatGPT Codex usage from /backend-api/wham/usage. Brute will still surface quota or plan errors when requests fail."
			response.Error = err.Error()
			return response
		}
		return usage
	case config.ProviderAnthropic:
		response.Source = "Claude Code CLI"
		if !s.providerConfiguredForUse(providerType) {
			response.Status = providerUsageStatusNotConfigured
			response.UsageLeftText = "Usage left unavailable — Claude Code CLI is not available. Install Claude Code or set AAGENT_CLAUDE_CLI_PATH."
			return response
		}
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable — Claude Code CLI does not expose remaining Anthropic plan or quota. Brute captures per-run Claude CLI token usage and total_cost_usd in session results, and will surface rate-limit, credits, billing, or budget errors when requests fail."
		return response
	default:
		response.Status = providerUsageStatusUnsupported
		response.Source = strings.TrimSpace(string(providerType))
		response.UsageLeftText = "Usage left is not supported for this provider yet."
		response.Refreshable = false
		return response
	}
}
