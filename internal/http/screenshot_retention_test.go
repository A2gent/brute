package http

import (
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestScreenshotGateOptInAndMissingCredentials(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("A2GENT_JEV_SCREENSHOTS_ENABLED", "")
	server, store := newClassifyRegistrationTestServer(t, config.Provider{APIKey: "jev-key"}, nil)
	if server.screenshotGate() != nil {
		t.Fatal("Jev screenshots must default off")
	}
	server.config.Tools.JevScreenshots = config.JevScreenshotsConfig{Enabled: true, Threshold: 0.4, MinConfidence: 0.9}
	gate := server.screenshotGate()
	if gate == nil || gate.Client == nil || gate.Threshold != 0.4 || gate.MinConfidence != 0.9 {
		t.Fatalf("configured gate = %+v", gate)
	}
	server.config.Providers["jev"] = config.Provider{}
	if gate = server.screenshotGate(); gate == nil || gate.Client != nil {
		t.Fatalf("missing credentials must preserve fail-open opt-in: %+v", gate)
	}
	if err := store.SaveSettings(map[string]string{jevScreenshotsSettingKey: "false"}); err != nil {
		t.Fatal(err)
	}
	if server.screenshotGate() != nil || server.settingsResponseWithJev(nil, nil).Settings[jevScreenshotsSettingKey] != "false" {
		t.Fatal("saved app setting opt-out ignored")
	}
	customEnv, err := store.GetCustomEnv()
	if err != nil || customEnv[jevScreenshotsSettingKey] != "" {
		t.Fatal("managed screenshot flag leaked to custom environment")
	}
	t.Setenv("A2GENT_JEV_SCREENSHOTS_ENABLED", "false")
	if server.screenshotGate() != nil {
		t.Fatal("operator opt-out ignored")
	}
}
