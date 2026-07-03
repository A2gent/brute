package http

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/codexauth"
	"github.com/A2gent/brute/internal/config"
)

type OpenAICodexOAuthImportRequest struct {
	Path string `json:"path,omitempty"`
}

type OpenAICodexOAuthImportResponse struct {
	Success   bool   `json:"success"`
	Imported  bool   `json:"imported"`
	Path      string `json:"path"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

type ProviderOAuthStatusResponse struct {
	Enabled   bool  `json:"enabled"`
	ExpiresAt int64 `json:"expires_at,omitempty"`
}

// handleOpenAICodexOAuthImport imports OAuth tokens from Codex auth cache.
func (s *Server) handleOpenAICodexOAuthImport(w http.ResponseWriter, r *http.Request) {
	var req OpenAICodexOAuthImportRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}
	}

	oauth, authPath, err := codexauth.Load(strings.TrimSpace(req.Path))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to import Codex OAuth: "+err.Error())
		return
	}

	provider := s.config.Providers[string(config.ProviderOpenAICodex)]
	provider.OAuth = oauth
	s.config.Providers[string(config.ProviderOpenAICodex)] = provider

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save OAuth tokens: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, OpenAICodexOAuthImportResponse{
		Success:   true,
		Imported:  true,
		Path:      authPath,
		ExpiresAt: oauth.ExpiresAt,
	})
}

func (s *Server) handleOpenAICodexOAuthStatus(w http.ResponseWriter, r *http.Request) {
	// Keep the connected OAuth snapshot aligned with Codex CLI's local cache so
	// Caesar does not show stale expiry data after Codex refreshes the token.
	s.syncOpenAICodexOAuthFromCache()

	provider := s.config.Providers[string(config.ProviderOpenAICodex)]
	if provider.OAuth == nil || strings.TrimSpace(provider.OAuth.AccessToken) == "" {
		s.jsonResponse(w, http.StatusOK, ProviderOAuthStatusResponse{Enabled: false})
		return
	}
	s.jsonResponse(w, http.StatusOK, ProviderOAuthStatusResponse{
		Enabled:   true,
		ExpiresAt: provider.OAuth.ExpiresAt,
	})
}

func (s *Server) handleOpenAICodexOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	provider := s.config.Providers[string(config.ProviderOpenAICodex)]
	provider.OAuth = nil
	s.config.Providers[string(config.ProviderOpenAICodex)] = provider

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save config: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"success": true})
}
