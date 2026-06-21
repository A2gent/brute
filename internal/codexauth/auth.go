package codexauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
)

func DefaultPath() string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return filepath.Join(codexHome, "auth.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/.codex/auth.json"
	}
	return filepath.Join(home, ".codex", "auth.json")
}

func Load(path string) (*config.OAuthConfig, string, error) {
	authPath := strings.TrimSpace(path)
	if authPath == "" {
		authPath = DefaultPath()
	}
	authPath = expandHome(authPath)

	raw, err := os.ReadFile(authPath)
	if err != nil {
		return nil, authPath, fmt.Errorf("failed to read Codex auth file: %w", err)
	}

	var payload interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, authPath, fmt.Errorf("invalid Codex auth JSON: %w", err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err == nil {
		return nil, authPath, fmt.Errorf("invalid Codex auth JSON: multiple JSON values")
	} else if err != io.EOF {
		return nil, authPath, fmt.Errorf("invalid Codex auth JSON: %w", err)
	}

	accessToken := deepFindString(payload, map[string]struct{}{
		"access_token": {},
		"accesstoken":  {},
		"token":        {},
	})
	if accessToken == "" {
		return nil, authPath, fmt.Errorf("no access token found in Codex auth file")
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
		return nil, authPath, fmt.Errorf("imported OAuth token is expired; run codex login again")
	}

	return &config.OAuthConfig{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
	}, authPath, nil
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
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
	switch {
	case ts > 1_000_000_000_000_000_000:
		return ts / 1_000_000_000
	case ts > 1_000_000_000_000_000:
		return ts / 1_000_000
	case ts > 1_000_000_000_000:
		return ts / 1_000
	default:
		return ts
	}
}
