package http

import (
	"os"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestToolResultCompressionEnabled_DefaultsToTrueWithoutEnvOrSetting(t *testing.T) {
	// WHY: toolResultCompressionEnabled intentionally lets the environment win,
	// so this DB/default test must not inherit a developer shell override.
	t.Setenv(toolResultCompressionSettingKey, "")

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)
	if !server.toolResultCompressionEnabled() {
		t.Fatalf("expected tool result compression to default to enabled")
	}
}

func TestToolResultCompressionEnabled_SettingFalseDisablesFeature(t *testing.T) {
	// WHY: this test verifies persisted settings, not env override behavior.
	t.Setenv(toolResultCompressionSettingKey, "")

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()
	if err := store.SaveSettings(map[string]string{toolResultCompressionSettingKey: "false"}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)
	if server.toolResultCompressionEnabled() {
		t.Fatalf("expected explicit false setting to disable tool result compression")
	}
}

func TestSettingsResponseExposesCompressionEnabledByDefault(t *testing.T) {
	resp := settingsResponse(map[string]string{}, nil)
	if got := resp.Settings[toolResultCompressionSettingKey]; got != "true" {
		t.Fatalf("settings response default = %q, want true", got)
	}
}

func TestSettingsResponseExposesBrowserChromeHeadlessByDefault(t *testing.T) {
	resp := settingsResponse(map[string]string{}, nil)
	if got := resp.Settings[storage.BrowserChromeHeadlessSettingKey]; got != "true" {
		t.Fatalf("browser Chrome headless default = %q, want true", got)
	}
}

func TestSettingsResponseHidesBranchTaskDocAppSettings(t *testing.T) {
	resp := settingsResponse(map[string]string{
		legacyBranchTaskDocDirectorySettingPrefix + "project-1": "/tmp/docs",
		legacyBranchTaskDocModeSettingPrefix + "project-1":      "content",
		projectBranchTaskDocDirectorySettingKey:                 "/tmp/global-docs",
		projectBranchTaskDocModeSettingKey:                      "path",
		"A2A_REGISTRY_URL":                                      "https://a2gent.net",
	}, nil)

	for key := range resp.Settings {
		if isBranchTaskDocAppSettingKey(key) {
			t.Fatalf("settings response exposed project-scoped branch task doc key %q", key)
		}
	}
	if got := resp.Settings["A2A_REGISTRY_URL"]; got != "https://a2gent.net" {
		t.Fatalf("settings response dropped unrelated setting: %q", got)
	}
}

func TestSyncSettingsToEnvOnlyAppliesExplicitCustomEnv(t *testing.T) {
	t.Setenv("A2GENT_MANAGED_APP_SETTING", "")
	t.Setenv("A2GENT_TEST_VISIBLE_SETTING", "")

	syncCustomEnvToEnv(
		map[string]string{"A2GENT_REMOVED_CUSTOM_ENV": "old"},
		map[string]string{"A2GENT_TEST_VISIBLE_SETTING": "visible"},
	)

	if got := os.Getenv("A2GENT_TEST_VISIBLE_SETTING"); got != "visible" {
		t.Fatalf("custom env setting = %q, want visible", got)
	}
	if got := os.Getenv("A2GENT_MANAGED_APP_SETTING"); got != "" {
		t.Fatalf("managed app setting leaked to env: %q", got)
	}
}
