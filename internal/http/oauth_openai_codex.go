package http

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// handleOpenAICodexOAuthImport imports OAuth tokens from Codex auth cache.
func (s *Server) handleOpenAICodexOAuthImport(w http.ResponseWriter, r *http.Request) {
	var req OpenAICodexOAuthImportRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
			return
		}
	}

	authPath := defaultOpenAICodexAuthPath()
	if strings.TrimSpace(req.Path) != "" {
		authPath = strings.TrimSpace(req.Path)
	}
	if strings.HasPrefix(authPath, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to resolve home directory: "+err.Error())
			return
		}
		authPath = filepath.Join(home, strings.TrimPrefix(authPath, "~/"))
	}

	raw, err := os.ReadFile(authPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read Codex auth file. Run `codex login` first, then retry import: "+err.Error())
		return
	}

	var payload interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid Codex auth JSON: "+err.Error())
		return
	}

	accessToken := deepFindString(payload, map[string]struct{}{
		"access_token": {},
		"accesstoken":  {},
		"token":        {},
	})
	if accessToken == "" {
		s.errorResponse(w, http.StatusBadRequest, "No access token found in Codex auth file")
		return
	}

	refreshToken := deepFindString(payload, map[string]struct{}{
		"refresh_token": {},
		"refreshtoken":  {},
	})
	expiresAt := deepFindTimestamp(payload, map[string]struct{}{
		"expires_at": {},
		"expiresat":  {},
		"expiry":     {},
		"expires":    {},
	})
	if expiresAt > 0 && expiresAt < time.Now().Unix() {
		s.errorResponse(w, http.StatusBadRequest, "Imported OAuth token is expired. Run `codex login` again, then retry import.")
		return
	}

	provider := s.config.Providers[string(config.ProviderOpenAICodex)]
	provider.OAuth = &config.OAuthConfig{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
	}
	s.config.Providers[string(config.ProviderOpenAICodex)] = provider

	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save OAuth tokens: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, OpenAICodexOAuthImportResponse{
		Success:   true,
		Imported:  true,
		Path:      authPath,
		ExpiresAt: expiresAt,
	})
}

func (s *Server) handleOpenAICodexOAuthStatus(w http.ResponseWriter, r *http.Request) {
	provider := s.config.Providers[string(config.ProviderOpenAICodex)]
	if provider.OAuth == nil || strings.TrimSpace(provider.OAuth.AccessToken) == "" {
		s.jsonResponse(w, http.StatusOK, AnthropicOAuthStatusResponse{Enabled: false})
		return
	}
	s.jsonResponse(w, http.StatusOK, AnthropicOAuthStatusResponse{
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

func defaultOpenAICodexAuthPath() string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return filepath.Join(codexHome, "auth.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/.codex/auth.json"
	}
	return filepath.Join(home, ".codex", "auth.json")
}

func deepFindString(value interface{}, keySet map[string]struct{}) string {
	switch v := value.(type) {
	case map[string]interface{}:
		for key, val := range v {
			if _, ok := keySet[strings.ToLower(strings.TrimSpace(key))]; ok {
				if s := parseString(val); s != "" {
					return s
				}
			}
		}
		for _, val := range v {
			if s := deepFindString(val, keySet); s != "" {
				return s
			}
		}
	case []interface{}:
		for _, item := range v {
			if s := deepFindString(item, keySet); s != "" {
				return s
			}
		}
	}
	return ""
}

func deepFindTimestamp(value interface{}, keySet map[string]struct{}) int64 {
	switch v := value.(type) {
	case map[string]interface{}:
		for key, val := range v {
			if _, ok := keySet[strings.ToLower(strings.TrimSpace(key))]; ok {
				if ts := parseTimestamp(val); ts > 0 {
					return ts
				}
			}
		}
		for _, val := range v {
			if ts := deepFindTimestamp(val, keySet); ts > 0 {
				return ts
			}
		}
	case []interface{}:
		for _, item := range v {
			if ts := deepFindTimestamp(item, keySet); ts > 0 {
				return ts
			}
		}
	}
	return 0
}

func parseString(value interface{}) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

func parseTimestamp(value interface{}) int64 {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		return normalizeUnixTS(int64(v))
	case int64:
		return normalizeUnixTS(v)
	case int:
		return normalizeUnixTS(int64(v))
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return normalizeUnixTS(i)
		}
		if f, err := v.Float64(); err == nil {
			return normalizeUnixTS(int64(f))
		}
	case string:
		raw := strings.TrimSpace(v)
		if raw == "" {
			return 0
		}
		if i, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return normalizeUnixTS(i)
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func normalizeUnixTS(ts int64) int64 {
	// Convert milliseconds to seconds when needed.
	if ts > 9999999999 {
		return ts / 1000
	}
	return ts
}
