package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func runWaitUntil(t *testing.T, ctx context.Context, dir string, params map[string]interface{}) *Result {
	t.Helper()
	raw, _ := json.Marshal(params)
	result, err := NewWaitUntilTool(dir).Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return result
}

func TestWaitUntilMetOnSecondAttempt(t *testing.T) {
	dir := t.TempDir()
	result := runWaitUntil(t, context.Background(), dir, map[string]interface{}{
		"command":          "if [ -f marker ]; then echo ready; exit 0; fi; touch marker; exit 1",
		"interval_seconds": 1,
		"timeout_seconds":  10,
	})
	if !result.Success || result.Metadata["attempts"] != 2 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Output, "condition met after 2 attempt(s)") || !strings.Contains(result.Output, "ready") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestWaitUntilTimeout(t *testing.T) {
	start := time.Now()
	result := runWaitUntil(t, context.Background(), t.TempDir(), map[string]interface{}{
		"command":          "echo not yet; exit 1",
		"interval_seconds": 1,
		"timeout_seconds":  1,
	})
	if result.Success || !strings.HasPrefix(result.Output, "timeout after") || !strings.Contains(result.Output, "not yet") {
		t.Fatalf("result = %+v", result)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestWaitUntilCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	result := runWaitUntil(t, ctx, t.TempDir(), map[string]interface{}{
		"command":         "sleep 30",
		"timeout_seconds": 60,
	})
	if result.Success || result.Error != "wait canceled" {
		t.Fatalf("result = %+v", result)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancel took %v", elapsed)
	}
}
