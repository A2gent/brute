package http

import (
	"encoding/json"
	"net/http"

	"github.com/A2gent/brute/internal/config"
)

type browserActSettingsResponse struct {
	Enabled       bool `json:"enabled"`
	JevConfigured bool `json:"jev_configured"`
}

type updateBrowserActSettingsRequest struct {
	Enabled *bool `json:"enabled"`
}

func (s *Server) browserActSettings() browserActSettingsResponse {
	apiKey, _, _ := s.resolveClassifyCredentials()
	return browserActSettingsResponse{
		Enabled:       s.config != nil && s.config.Tools.BrowserActEnabled,
		JevConfigured: apiKey != "",
	}
}

func (s *Server) handleGetBrowserActSettings(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, s.browserActSettings())
}

// handleUpdateBrowserActSettings persists config.tools.browser_act_enabled and re-registers the
// tool so the change applies without a restart. Enabling without a Jev key is allowed; the tool
// stays absent until credentials exist.
func (s *Server) handleUpdateBrowserActSettings(w http.ResponseWriter, r *http.Request) {
	var req updateBrowserActSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.Enabled == nil {
		s.errorResponse(w, http.StatusBadRequest, "enabled is required")
		return
	}
	if s.config == nil {
		s.errorResponse(w, http.StatusInternalServerError, "Configuration is unavailable")
		return
	}

	previous := s.config.Tools.BrowserActEnabled
	s.config.Tools.BrowserActEnabled = *req.Enabled
	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.config.Tools.BrowserActEnabled = previous
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save config: "+err.Error())
		return
	}
	s.registerBrowserActTool(s.toolManager)
	s.jsonResponse(w, http.StatusOK, s.browserActSettings())
}
