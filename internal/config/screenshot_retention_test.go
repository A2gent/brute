package config

import (
	"encoding/json"
	"testing"
)

func TestScreenshotRetentionConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tools.ScreenshotKeepLatest != 2 || cfg.Tools.JevScreenshots.Enabled {
		t.Fatal("keep latest two by default; Jev screenshots must be opt-in")
	}
	if err := json.Unmarshal([]byte(`{"tools":{"screenshot_keep_latest":4,"jev_screenshots":{"enabled":true,"threshold":0.4,"min_confidence":0.9}}}`), cfg); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := DefaultConfig()
	if err := json.Unmarshal(encoded, reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.Tools.ScreenshotKeepLatest != 4 || reloaded.Tools.JevScreenshots != (JevScreenshotsConfig{Enabled: true, Threshold: 0.4, MinConfidence: 0.9}) {
		t.Fatalf("policy did not survive reload: %+v", reloaded.Tools)
	}
}
