package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSuggestSessionTool_Execute(t *testing.T) {
	tool := NewSuggestSessionTool()

	t.Run("formats valid action card fence", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"title":    "Fix cache invalidation",
			"label":    "Create fix session",
			"severity": "HIGH",
			"files":    []string{" src/cache.ts ", "src/cache.ts", "src/api.ts"},
			"prompt":   "Implement the cache invalidation fix and verify with tests.",
		})

		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if !strings.HasPrefix(result.Output, "```a2gent-session\n") || !strings.HasSuffix(result.Output, "\n```") {
			t.Fatalf("expected a2gent-session fenced block, got %q", result.Output)
		}
		if !strings.Contains(result.Output, `"severity": "high"`) {
			t.Fatalf("expected normalized severity, got %q", result.Output)
		}
		if strings.Count(result.Output, "src/cache.ts") != 1 {
			t.Fatalf("expected duplicate files to be removed, got %q", result.Output)
		}

		metadataCard, ok := result.Metadata["card"].(SuggestSessionParams)
		if !ok {
			t.Fatalf("expected card metadata, got %#v", result.Metadata["card"])
		}
		if metadataCard.Title != "Fix cache invalidation" || metadataCard.Label != "Create fix session" {
			t.Fatalf("unexpected card metadata: %#v", metadataCard)
		}
	})

	t.Run("requires title and prompt", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"title":  " ",
			"prompt": "Implement something",
		})

		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success || !strings.Contains(result.Error, "title is required") {
			t.Fatalf("expected title validation failure, got %#v", result)
		}
	})

	t.Run("validates severity", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"title":    "Follow up",
			"prompt":   "Investigate follow-up.",
			"severity": "critical",
		})

		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success || !strings.Contains(result.Error, "severity must be") {
			t.Fatalf("expected severity validation failure, got %#v", result)
		}
	})
}
