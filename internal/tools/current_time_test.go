package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCurrentTimeTool_Execute(t *testing.T) {
	fixed := time.Date(2026, time.April, 12, 10, 11, 12, 0, time.FixedZone("EEST", 3*60*60))
	tool := &CurrentTimeTool{
		now: func() time.Time { return fixed },
	}

	t.Run("uses system location by default", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), nil)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if result.Output != "2026-04-12T10:11:12+03:00" {
			t.Fatalf("unexpected output: %q", result.Output)
		}
		if result.Metadata["timezone"] != "EEST" {
			t.Fatalf("unexpected timezone metadata: %#v", result.Metadata["timezone"])
		}
	})

	t.Run("converts to requested timezone", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"timezone": "UTC",
		})
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if result.Output != "2026-04-12T07:11:12Z" {
			t.Fatalf("unexpected output: %q", result.Output)
		}
		if result.Metadata["timezone"] != "UTC" {
			t.Fatalf("unexpected timezone metadata: %#v", result.Metadata["timezone"])
		}
	})

	t.Run("rejects invalid timezone", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"timezone": "Mars/Base",
		})
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
	})
}
