package config

import (
	"encoding/json"
	"testing"
)

func TestRelevanceGateDisableConfigRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tools.RelevanceGateDisabled {
		t.Fatal("gate must default to enabled")
	}
	if err := json.Unmarshal([]byte(`{"tools":{"relevance_gate_disabled":true}}`), cfg); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var saved Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Tools.RelevanceGateDisabled {
		t.Fatal("disable switch lost on round trip")
	}
}
