package config

import (
	"encoding/json"
	"testing"
)

func TestJevEffortOptIn(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tools.JevEffort.Enabled {
		t.Fatal("Jev effort must be opt-in")
	}
	if err := json.Unmarshal([]byte(`{"tools":{"jev_effort":{"enabled":true}}}`), cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := DefaultConfig()
	if err := json.Unmarshal(raw, reloaded); err != nil {
		t.Fatal(err)
	}
	if !reloaded.Tools.JevEffort.Enabled {
		t.Fatal("Jev effort opt-in was not persisted")
	}
}
