package storage

import "testing"

func TestSaveSettingsSeparatesManagedSettingsFromCustomEnv(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()
	if err := store.SaveSettingsAndCustomEnv(map[string]string{
		"A2A_REGISTRY_URL":     "https://registry.example",
		"AAGENT_SKILLS_FOLDER": "/tmp/skills",
		"CUSTOM_TOOL_TOKEN":    "secret",
	}, map[string]string{"EXPLICIT_CUSTOM": "ok"}); err != nil {
		t.Fatalf("SaveSettingsAndCustomEnv: %v", err)
	}
	settings, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings["A2A_REGISTRY_URL"] != "https://registry.example" || settings["AAGENT_SKILLS_FOLDER"] != "/tmp/skills" {
		t.Fatalf("managed setting missing: %#v", settings)
	}
	if _, ok := settings["CUSTOM_TOOL_TOKEN"]; ok {
		t.Fatalf("custom env leaked into app_settings: %#v", settings)
	}
	customEnv, err := store.GetCustomEnv()
	if err != nil {
		t.Fatalf("GetCustomEnv: %v", err)
	}
	if customEnv["CUSTOM_TOOL_TOKEN"] != "secret" || customEnv["EXPLICIT_CUSTOM"] != "ok" {
		t.Fatalf("custom env mismatch: %#v", customEnv)
	}
}

func TestBrowserChromeHeadlessIsManagedSetting(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	if err := store.SaveSettingsAndCustomEnv(map[string]string{
		BrowserChromeHeadlessSettingKey: "false",
	}, nil); err != nil {
		t.Fatalf("SaveSettingsAndCustomEnv: %v", err)
	}

	settings, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got := settings[BrowserChromeHeadlessSettingKey]; got != "false" {
		t.Fatalf("managed browser setting = %q, want false", got)
	}
	customEnv, err := store.GetCustomEnv()
	if err != nil {
		t.Fatalf("GetCustomEnv: %v", err)
	}
	if _, exists := customEnv[BrowserChromeHeadlessSettingKey]; exists {
		t.Fatalf("browser setting leaked into custom env: %#v", customEnv)
	}
}

func TestMigrateBrowserChromeHeadlessFromCustomEnv(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(`
		INSERT INTO custom_env (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
	`, BrowserChromeHeadlessSettingKey, "false"); err != nil {
		t.Fatalf("insert legacy browser setting: %v", err)
	}
	if err := store.migrateBrowserChromeHeadlessSetting(); err != nil {
		t.Fatalf("migrateBrowserChromeHeadlessSetting: %v", err)
	}

	settings, _ := store.GetSettings()
	if got := settings[BrowserChromeHeadlessSettingKey]; got != "false" {
		t.Fatalf("migrated browser setting = %q, want false", got)
	}
	customEnv, _ := store.GetCustomEnv()
	if _, exists := customEnv[BrowserChromeHeadlessSettingKey]; exists {
		t.Fatalf("legacy browser setting remained in custom env: %#v", customEnv)
	}
}

func TestMigrateLegacyCustomEnvFromAppSettings(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	_, err = store.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP), (?, ?, CURRENT_TIMESTAMP)`, "A2A_REGISTRY_URL", "https://registry.example", "CUSTOM_TOOL_TOKEN", "secret")
	if err != nil {
		t.Fatalf("insert legacy settings: %v", err)
	}
	if err := store.migrateLegacyCustomEnvFromAppSettings(); err != nil {
		t.Fatalf("migrateLegacyCustomEnvFromAppSettings: %v", err)
	}
	settings, _ := store.GetSettings()
	if settings["A2A_REGISTRY_URL"] == "" {
		t.Fatalf("managed setting was removed: %#v", settings)
	}
	if _, ok := settings["CUSTOM_TOOL_TOKEN"]; ok {
		t.Fatalf("legacy custom env remained in app_settings: %#v", settings)
	}
	customEnv, _ := store.GetCustomEnv()
	if customEnv["CUSTOM_TOOL_TOKEN"] != "secret" {
		t.Fatalf("custom env not migrated: %#v", customEnv)
	}
}
