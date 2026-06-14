package http

import (
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestToolResultCompressionEnabled_DefaultsToTrueWithoutEnvOrSetting(t *testing.T) {
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
	resp := settingsResponse(map[string]string{})
	if got := resp.Settings[toolResultCompressionSettingKey]; got != "true" {
		t.Fatalf("settings response default = %q, want true", got)
	}
}
