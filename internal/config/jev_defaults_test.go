package config

import (
	"encoding/json"
	"testing"
)

func TestJevDefaultsAndPersistedOptOut(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Tools.JevAdmission.Enabled || !cfg.Tools.JevToolsets.Enabled {
		t.Fatal("Jev automation must default on")
	}
	if err := json.Unmarshal([]byte(`{"tools":{"jev_admission":{"enabled":false},"jev_toolsets":{"enabled":false}}}`), cfg); err != nil {
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
	if reloaded.Tools.JevAdmission.Enabled || reloaded.Tools.JevToolsets.Enabled {
		t.Fatal("false must survive save and reload, not disappear via omitempty")
	}
}
