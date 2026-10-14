package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestJevAutomationDefaultsAndSavedOptOut(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv(jevAdmissionSettingKey, "")
	t.Setenv(jevToolsetsSettingKey, "")
	server, store := newClassifyRegistrationTestServer(t, config.Provider{APIKey: "key"}, nil)
	if server.relevanceGate() == nil || server.toolSelector() == nil {
		t.Fatal("Jev automation must be enabled by default with credentials")
	}
	// Settings take precedence over legacy config, including its old default false.
	server.config.Tools.JevAdmission.Enabled = false
	server.config.Tools.JevToolsets.Enabled = false
	for _, enabled := range []string{"true", "false", "true"} {
		rec := httptest.NewRecorder()
		server.handleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"settings":{"A2GENT_JEV_ADMISSION_ENABLED":"`+enabled+`","A2GENT_JEV_TOOLSETS_ENABLED":"`+enabled+`"}}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
		}
		var response SettingsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Settings[jevAdmissionSettingKey] != enabled || response.Settings[jevToolsetsSettingKey] != enabled {
			t.Fatal("settings not returned")
		}
		want := enabled == "true"
		if (server.relevanceGate() != nil) != want || (server.toolSelector() != nil) != want {
			t.Fatal("saved switch not applied to new runs")
		}
		saved, err := store.GetSettings()
		if err != nil || saved[jevAdmissionSettingKey] != enabled {
			t.Fatal("switch not persisted as app setting")
		}
	}
	t.Setenv(jevAdmissionSettingKey, "false")
	t.Setenv(jevToolsetsSettingKey, "false")
	if server.relevanceGate() != nil || server.toolSelector() != nil {
		t.Fatal("environment opt-out ignored")
	}
}

func TestJevAutomationMissingKeyAndSecretInventoryFailOpen(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv(jevAdmissionSettingKey, "")
	t.Setenv(jevToolsetsSettingKey, "")
	server, _ := newClassifyRegistrationTestServer(t, config.Provider{}, nil)
	if server.relevanceGate() != nil || server.toolSelector() != nil {
		t.Fatal("missing credentials must fail open")
	}
	server.config.Providers["jev"] = config.Provider{APIKey: "key"}
	server.store = webFailIntegrationStore{server.store}
	if server.relevanceGate() != nil || server.toolSelector() != nil {
		t.Fatal("incomplete secret inventory must fail open")
	}
}

func TestJevSettingsResponseDefaults(t *testing.T) {
	got := settingsResponse(nil, nil).Settings
	if got[jevAdmissionSettingKey] != "true" || got[jevToolsetsSettingKey] != "true" {
		t.Fatal("UI defaults must be enabled")
	}
}
