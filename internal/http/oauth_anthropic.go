package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/anthropic"
)

// AnthropicOAuthStartResponse contains the authorization URL
type AnthropicOAuthStartResponse struct {
	AuthURL  string `json:"auth_url"`
	Verifier string `json:"verifier"` // Stored client-side temporarily
}

// AnthropicOAuthCallbackRequest contains the authorization code from user
type AnthropicOAuthCallbackRequest struct {
	Code     string `json:"code"`
	Verifier string `json:"verifier"`
}

// AnthropicOAuthStatusResponse shows OAuth status
type AnthropicOAuthStatusResponse struct {
	Enabled   bool  `json:"enabled"`
	ExpiresAt int64 `json:"expires_at,omitempty"`
}

// handleAnthropicOAuthStart initiates OAuth flow
func (s *Server) handleAnthropicOAuthStart(w http.ResponseWriter, r *http.Request) {
	// Generate PKCE challenge
	pkce, err := anthropic.GeneratePKCEChallenge()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to generate PKCE challenge: "+err.Error())
		return
	}

	// Build authorization URL
	authURL := anthropic.BuildAuthorizationURL(pkce)

	s.jsonResponse(w, http.StatusOK, AnthropicOAuthStartResponse{
		AuthURL:  authURL,
		Verifier: pkce.Verifier, // Client needs to store this temporarily
	})
}

// handleAnthropicOAuthCallback handles OAuth callback with authorization code
func (s *Server) handleAnthropicOAuthCallback(w http.ResponseWriter, r *http.Request) {
	var req AnthropicOAuthCallbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Code == "" || req.Verifier == "" {
		s.errorResponse(w, http.StatusBadRequest, "Missing code or verifier")
		return
	}

	// Exchange code for tokens
	tokens, err := anthropic.ExchangeCodeForTokens(req.Code, req.Verifier)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to exchange code for tokens: "+err.Error())
		return
	}

	// Calculate expiration timestamp
	expiresAt := time.Now().Unix() + int64(tokens.ExpiresIn)

	// Save tokens to config
	provider := s.config.Providers[string(config.ProviderAnthropic)]
	provider.OAuth = &config.OAuthConfig{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    expiresAt,
	}
	s.config.Providers[string(config.ProviderAnthropic)] = provider

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save OAuth tokens: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"expires_at": expiresAt,
	})
}

// handleAnthropicOAuthStatus returns current OAuth status
func (s *Server) handleAnthropicOAuthStatus(w http.ResponseWriter, r *http.Request) {
	provider := s.config.Providers[string(config.ProviderAnthropic)]

	if provider.OAuth == nil || provider.OAuth.AccessToken == "" {
		s.jsonResponse(w, http.StatusOK, AnthropicOAuthStatusResponse{
			Enabled: false,
		})
		return
	}

	s.jsonResponse(w, http.StatusOK, AnthropicOAuthStatusResponse{
		Enabled:   true,
		ExpiresAt: provider.OAuth.ExpiresAt,
	})
}

// handleAnthropicOAuthDisconnect removes OAuth tokens
func (s *Server) handleAnthropicOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	provider := s.config.Providers[string(config.ProviderAnthropic)]
	provider.OAuth = nil
	s.config.Providers[string(config.ProviderAnthropic)] = provider

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save config: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"success": true,
	})
}

// refreshAnthropicOAuthToken is a helper function to refresh tokens
func (s *Server) refreshAnthropicOAuthToken(refreshToken string) (*anthropic.OAuthTokens, error) {
	newTokens, err := anthropic.RefreshAccessToken(refreshToken)
	if err != nil {
		return nil, err
	}

	// Save new tokens to config
	provider := s.config.Providers[string(config.ProviderAnthropic)]
	expiresAt := time.Now().Unix() + int64(newTokens.ExpiresIn)
	provider.OAuth = &config.OAuthConfig{
		AccessToken:  newTokens.AccessToken,
		RefreshToken: newTokens.RefreshToken,
		ExpiresAt:    expiresAt,
	}
	s.config.Providers[string(config.ProviderAnthropic)] = provider

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		return nil, fmt.Errorf("failed to save refreshed tokens: %w", err)
	}

	return newTokens, nil
}
