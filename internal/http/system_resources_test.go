package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCounterRate(t *testing.T) {
	if got := counterRate(100, 300, 2*time.Second); got != 100 {
		t.Fatalf("delta: got %v", got)
	}
	if got := counterRate(100, 100, time.Second); got != 0 {
		t.Fatalf("zero delta: got %v", got)
	}
	if got := counterRate(500, 100, time.Second); got != 0 {
		t.Fatalf("counter reset: got %v", got)
	}
	if got := counterRate(1, 2, 0); got != 0 {
		t.Fatalf("zero elapsed: got %v", got)
	}
}

func TestHandleSystemResourcesShape(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleSystemResources(rec, httptest.NewRequest(http.MethodGet, "/system/resources", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]map[string]any
	var top map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["captured_at"].(string); !ok {
		t.Fatal("captured_at missing")
	}
	raw, _ := json.Marshal(top)
	_ = json.Unmarshal(raw, &body)
	want := map[string][]string{
		"cpu":     {"usage_percent", "cores"},
		"memory":  {"total_bytes", "used_bytes", "available_bytes", "used_percent"},
		"gpu":     {"available", "name", "source", "memory_total_bytes", "memory_used_bytes", "utilization_percent"},
		"network": {"rx_bytes_per_sec", "tx_bytes_per_sec"},
		"disk_io": {"read_bytes_per_sec", "write_bytes_per_sec"},
		"disk":    {"path", "total_bytes", "free_bytes", "used_percent"},
	}
	for section, keys := range want {
		for _, k := range keys {
			if _, ok := body[section][k]; !ok {
				t.Errorf("missing %s.%s", section, k)
			}
		}
	}
}
