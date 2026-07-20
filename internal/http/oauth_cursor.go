package http

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/cursorauth"
)

type CursorOAuthImportRequest struct {
	Path string `json:"path,omitempty"`
}

type CursorOAuthImportResponse struct {
	Success  bool   `json:"success"`
	Imported bool   `json:"imported"`
	Path     string `json:"path"`
}

// handleCursorOAuthImport stores Cursor OAuth credentials for usage display and Docker children.
func (s *Server) handleCursorOAuthImport(w http.ResponseWriter, r *http.Request) {
	var req CursorOAuthImportRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}
	}

	oauth, authPath, err := cursorauth.Load(strings.TrimSpace(req.Path))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to import Cursor OAuth: "+err.Error())
		return
	}

	provider := s.config.Providers[string(config.ProviderCursor)]
	provider.OAuth = oauth
	s.config.Providers[string(config.ProviderCursor)] = provider

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save OAuth tokens: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, CursorOAuthImportResponse{
		Success:  true,
		Imported: true,
		Path:     authPath,
	})
}

func (s *Server) handleCursorOAuthStatus(w http.ResponseWriter, r *http.Request) {
	s.syncCursorOAuthFromPlatform()

	provider := s.config.Providers[string(config.ProviderCursor)]
	if provider.OAuth == nil || strings.TrimSpace(provider.OAuth.AccessToken) == "" {
		s.jsonResponse(w, http.StatusOK, ProviderOAuthStatusResponse{Enabled: false})
		return
	}
	s.jsonResponse(w, http.StatusOK, ProviderOAuthStatusResponse{Enabled: true})
}

func (s *Server) handleCursorOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	provider := s.config.Providers[string(config.ProviderCursor)]
	provider.OAuth = nil
	s.config.Providers[string(config.ProviderCursor)] = provider

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save config: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"success": true})
}

// syncCursorOAuthFromPlatform refreshes stored Cursor OAuth from the local login session.
func (s *Server) syncCursorOAuthFromPlatform() bool {
	provider := s.config.Providers[string(config.ProviderCursor)]
	if provider.OAuth != nil && strings.TrimSpace(provider.OAuth.AccessToken) != "" {
		return false
	}

	oauth, _, err := cursorauth.Load("")
	if err != nil || oauth == nil || strings.TrimSpace(oauth.AccessToken) == "" {
		return false
	}
	provider.OAuth = oauth
	s.config.Providers[string(config.ProviderCursor)] = provider
	if err := s.config.Save(config.GetConfigPath()); err != nil {
		return false
	}
	return true
}
