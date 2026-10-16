package config

import (
	"encoding/json"
	"testing"
)

func TestJevStallConfigOptIn(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"tools":{}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Tools.JevStall.Enabled {
		t.Fatal("stall checks must default off")
	}
	if err := json.Unmarshal([]byte(`{"tools":{"jev_stall":{"enabled":true,"interval":4,"window":6}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if policy := cfg.Tools.JevStall; !policy.Enabled || policy.Interval != 4 || policy.Window != 6 {
		t.Fatalf("policy=%+v", policy)
	}
}
