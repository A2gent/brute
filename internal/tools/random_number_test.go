package tools

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
)

func TestRandomNumberTool_Execute(t *testing.T) {
	tool := NewRandomNumberTool()

	t.Run("inclusive bounds", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"start": 3,
			"end":   3,
		})
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if result.Output != "3" {
			t.Fatalf("expected exact output 3, got %q", result.Output)
		}
	})

	t.Run("exclusive start inclusive end", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"start":         10,
			"end":           11,
			"include_start": false,
			"include_end":   true,
		})
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if result.Output != "11" {
			t.Fatalf("expected exact output 11, got %q", result.Output)
		}
	})

	t.Run("both bounds exclusive", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"start":         1,
			"end":           4,
			"include_start": false,
			"include_end":   false,
		})
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		value, err := strconv.Atoi(result.Output)
		if err != nil {
			t.Fatalf("expected integer output, got %q", result.Output)
		}
		if value < 2 || value > 3 {
			t.Fatalf("expected value in (1,4), got %d", value)
		}
	})

	t.Run("empty range after exclusivity", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"start":         5,
			"end":           5,
			"include_start": false,
			"include_end":   false,
		})
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
	})

	t.Run("start greater than end", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"start": 9,
			"end":   2,
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
