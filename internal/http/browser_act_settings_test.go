package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func browserActSettingsRequest(t *testing.T, server *Server, method, body string) (*httptest.ResponseRecorder, browserActSettingsResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/browser-act/settings", strings.NewReader(body))
	if method == http.MethodPut {
		server.handleUpdateBrowserActSettings(rec, req)
	} else {
		server.handleGetBrowserActSettings(rec, req)
	}
	var out browserActSettingsResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return rec, out
}

func TestBrowserActSettingsDefaultOff(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("AAGENT_CONFIG_PATH", filepath.Join(t.TempDir(), "config.json"))
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{}, nil)

	rec, got := browserActSettingsRequest(t, server, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got.Enabled || got.JevConfigured {
		t.Fatalf("expected default off and no Jev key, got %+v", got)
	}
}

func TestBrowserActSettingsEnablePersistsAndRegisters(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv(disableToolsByDefaultSettingKey, "false")
	t.Setenv(syncDisabledToolsFromEnvSettingKey, "false")
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AAGENT_CONFIG_PATH", configPath)
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{APIKey: "key"}, nil)

	rec, got := browserActSettingsRequest(t, server, http.MethodPut, `{"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !got.Enabled || !got.JevConfigured {
		t.Fatalf("unexpected response %+v", got)
	}
	if _, ok := server.toolManager.Get("browser_act"); !ok {
		t.Fatal("enabling must register browser_act immediately")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config not persisted: %v", err)
	}
	var saved struct {
		Tools struct {
			BrowserActEnabled bool `json:"browser_act_enabled"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &saved); err != nil || !saved.Tools.BrowserActEnabled {
		t.Fatalf("config.tools.browser_act_enabled not persisted: err=%v data=%s", err, data)
	}

	rec, got = browserActSettingsRequest(t, server, http.MethodPut, `{"enabled":false}`)
	if rec.Code != http.StatusOK || got.Enabled {
		t.Fatalf("disable failed: %d %+v", rec.Code, got)
	}
	if _, ok := server.toolManager.Get("browser_act"); ok {
		t.Fatal("disabling must unregister browser_act")
	}
}

func TestBrowserActSettingsEnableWithoutJevKeyStaysUnavailable(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("AAGENT_CONFIG_PATH", filepath.Join(t.TempDir(), "config.json"))
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{}, nil)

	_, got := browserActSettingsRequest(t, server, http.MethodPut, `{"enabled":true}`)
	if !got.Enabled || got.JevConfigured {
		t.Fatalf("expected enabled but not configured, got %+v", got)
	}
	if _, ok := server.toolManager.Get("browser_act"); ok {
		t.Fatal("browser_act must stay absent without a Jev key")
	}
}

func TestBrowserActSettingsRejectsInvalidBody(t *testing.T) {
	t.Setenv("AAGENT_CONFIG_PATH", filepath.Join(t.TempDir(), "config.json"))
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{}, nil)
	for _, body := range []string{`not json`, `{}`} {
		rec, _ := browserActSettingsRequest(t, server, http.MethodPut, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
	if server.config.Tools.BrowserActEnabled {
		t.Fatal("invalid requests must not change the flag")
	}
}
