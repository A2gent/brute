package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func executeShowHTML(t *testing.T, args map[string]interface{}) *Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	result, err := NewShowHTMLTool().Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return result
}

func TestShowHTMLTool_Execute(t *testing.T) {
	t.Run("accepts html and applies default height", func(t *testing.T) {
		result := executeShowHTML(t, map[string]interface{}{
			"title": " Prototype ",
			"html":  " <div>Hello</div> ",
		})
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if result.Metadata["kind"] != "html_widget" {
			t.Fatalf("unexpected metadata: %#v", result.Metadata)
		}
		if result.Metadata["title"] != "Prototype" {
			t.Fatalf("expected trimmed title, got %#v", result.Metadata["title"])
		}
		if result.Metadata["height"] != showHTMLDefaultHeight {
			t.Fatalf("expected default height, got %#v", result.Metadata["height"])
		}
		if strings.Contains(result.Output, "<div>") {
			t.Fatalf("output must not echo the html, got %q", result.Output)
		}
	})

	t.Run("clamps height", func(t *testing.T) {
		low := executeShowHTML(t, map[string]interface{}{"html": "<p>x</p>", "height": 5})
		if low.Metadata["height"] != showHTMLMinHeight {
			t.Fatalf("expected min height, got %#v", low.Metadata["height"])
		}
		high := executeShowHTML(t, map[string]interface{}{"html": "<p>x</p>", "height": 99999})
		if high.Metadata["height"] != showHTMLMaxHeight {
			t.Fatalf("expected max height, got %#v", high.Metadata["height"])
		}
	})

	t.Run("requires html", func(t *testing.T) {
		result := executeShowHTML(t, map[string]interface{}{"html": "  "})
		if result.Success || !strings.Contains(result.Error, "html is required") {
			t.Fatalf("expected validation failure, got %#v", result)
		}
	})

	t.Run("rejects oversized html", func(t *testing.T) {
		result := executeShowHTML(t, map[string]interface{}{"html": strings.Repeat("a", showHTMLMaxBytes+1)})
		if result.Success || !strings.Contains(result.Error, "too large") {
			t.Fatalf("expected size failure, got %#v", result)
		}
	})
}
