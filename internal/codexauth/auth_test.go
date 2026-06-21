package codexauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDefaultPathUsesCodexHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", "  "+dir+"  ")

	if got, want := DefaultPath(), filepath.Join(dir, "auth.json"); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestLoadFindsNestedOAuthTokensAndNormalizesExpiry(t *testing.T) {
	dir := t.TempDir()
	expiresAt := time.Now().Add(2 * time.Hour).Unix()
	expiresAtMillis := json.Number(strconv.FormatInt(expiresAt*1000, 10))
	path := filepath.Join(dir, "auth.json")
	writeJSONFile(t, path, map[string]any{
		"accounts": []any{
			map[string]any{"metadata": "ignored"},
			map[string]any{
				"tokens": map[string]any{
					" access_token ": "  access-token  ",
					"refreshToken":   "  refresh-token  ",
					"expires":        expiresAtMillis,
				},
			},
		},
	})

	oauth, resolvedPath, err := Load("  " + path + "  ")
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if resolvedPath != path {
		t.Fatalf("resolved path = %q, want %q", resolvedPath, path)
	}
	if oauth.AccessToken != "access-token" {
		t.Fatalf("AccessToken = %q, want access-token", oauth.AccessToken)
	}
	if oauth.RefreshToken != "refresh-token" {
		t.Fatalf("RefreshToken = %q, want refresh-token", oauth.RefreshToken)
	}
	if oauth.ExpiresAt != expiresAt {
		t.Fatalf("ExpiresAt = %d, want normalized timestamp %d", oauth.ExpiresAt, expiresAt)
	}
}

func TestLoadUsesCodeXHomeDefaultPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	path := filepath.Join(dir, "auth.json")
	writeJSONFile(t, path, map[string]any{
		"tokens": map[string]any{
			"access_token": "default-path-token",
		},
	})

	oauth, resolvedPath, err := Load("")
	if err != nil {
		t.Fatalf("Load(default path) returned error: %v", err)
	}
	if resolvedPath != path {
		t.Fatalf("resolved path = %q, want %q", resolvedPath, path)
	}
	if oauth.AccessToken != "default-path-token" {
		t.Fatalf("AccessToken = %q, want default-path-token", oauth.AccessToken)
	}
}

func TestLoadExpandsHomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir Codex dir: %v", err)
	}
	path := filepath.Join(codexDir, "auth.json")
	writeJSONFile(t, path, map[string]any{
		"accessToken": "home-token",
	})

	oauth, resolvedPath, err := Load("~/.codex/auth.json")
	if err != nil {
		t.Fatalf("Load(home path) returned error: %v", err)
	}
	if resolvedPath != path {
		t.Fatalf("resolved path = %q, want %q", resolvedPath, path)
	}
	if oauth.AccessToken != "home-token" {
		t.Fatalf("AccessToken = %q, want home-token", oauth.AccessToken)
	}
}

func TestLoadRejectsExpiredToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeJSONFile(t, path, map[string]any{
		"tokens": map[string]any{
			"access_token": "expired-token",
			"expires_at":   time.Now().Add(-time.Hour).Unix(),
		},
	})

	_, _, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Load(expired token) error = %v, want expired token error", err)
	}
}

func TestLoadReportsInvalidInputs(t *testing.T) {
	tests := []struct {
		name        string
		contents    string
		wantMessage string
	}{
		{
			name:        "invalid JSON",
			contents:    `{`,
			wantMessage: "invalid Codex auth JSON",
		},
		{
			name:        "missing access token",
			contents:    `{"tokens":{"refresh_token":"refresh-token"}}`,
			wantMessage: "no access token found",
		},
		{
			name:        "multiple JSON values",
			contents:    `{"access_token":"token"} {}`,
			wantMessage: "multiple JSON values",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(path, []byte(tt.contents), 0o600); err != nil {
				t.Fatalf("write auth file: %v", err)
			}

			_, _, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantMessage) {
				t.Fatalf("Load() error = %v, want message containing %q", err, tt.wantMessage)
			}
		})
	}
}

func TestLoadReportsReadErrorWithResolvedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-auth.json")
	_, resolvedPath, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "failed to read Codex auth file") {
		t.Fatalf("Load(missing file) error = %v, want read error", err)
	}
	if resolvedPath != path {
		t.Fatalf("resolved path = %q, want %q", resolvedPath, path)
	}
}

func TestParseTimestamp(t *testing.T) {
	rfc3339 := "2030-01-02T03:04:05Z"
	parsed, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		value any
		want  int64
	}{
		{name: "seconds int", value: int64(1_700_000_000), want: 1_700_000_000},
		{name: "milliseconds string", value: "1700000000000", want: 1_700_000_000},
		{name: "microseconds number", value: json.Number("1700000000000000"), want: 1_700_000_000},
		{name: "nanoseconds float", value: float64(1_700_000_000_000_000_000), want: 1_700_000_000},
		{name: "rfc3339", value: rfc3339, want: parsed.Unix()},
		{name: "empty string", value: "   ", want: 0},
		{name: "invalid string", value: "not-a-time", want: 0},
		{name: "invalid number", value: json.Number("not-a-number"), want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseTimestamp(tt.value); got != tt.want {
				t.Fatalf("parseTimestamp(%v) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func writeJSONFile(t *testing.T, path string, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal auth file: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
}
