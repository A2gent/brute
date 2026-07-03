package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestManToolReturnsToolManual(t *testing.T) {
	manager := NewManager(t.TempDir())
	man, ok := manager.Get("man")
	if !ok {
		t.Fatalf("expected man tool to be registered")
	}

	result, err := man.Execute(context.Background(), json.RawMessage(`{"tool":"read"}`))
	if err != nil {
		t.Fatalf("man execute returned error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful man result, got %#v", result)
	}

	if !strings.Contains(result.Output, "# read") {
		t.Fatalf("expected read manual header, got: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Read file contents") {
		t.Fatalf("expected read description, got: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Input schema") {
		t.Fatalf("expected input schema section, got: %s", result.Output)
	}
	if !strings.Contains(result.Output, "include_line_numbers") {
		t.Fatalf("expected schema details, got: %s", result.Output)
	}
}

func TestManToolListsAvailableTools(t *testing.T) {
	manager := NewManager(t.TempDir())
	man, ok := manager.Get("man")
	if !ok {
		t.Fatalf("expected man tool to be registered")
	}

	result, err := man.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("man execute returned error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful man result, got %#v", result)
	}
	if !strings.Contains(result.Output, "Available tool manuals:") {
		t.Fatalf("expected list header, got: %s", result.Output)
	}
	if !strings.Contains(result.Output, "- man") || !strings.Contains(result.Output, "- read") {
		t.Fatalf("expected tool names in list, got: %s", result.Output)
	}
	if strings.Contains(result.Output, "Read file contents") {
		t.Fatalf("expected list to contain names only, got: %s", result.Output)
	}
}

func TestManToolUsesCurrentClonedManagerState(t *testing.T) {
	manager := NewManager(t.TempDir())
	cloned := manager.Clone()
	cloned.Unregister("read")

	man, ok := cloned.Get("man")
	if !ok {
		t.Fatalf("expected man tool to be registered on clone")
	}

	result, err := man.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("man execute returned error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful man result, got %#v", result)
	}
	if strings.Contains(result.Output, "- read") {
		t.Fatalf("expected cloned man to respect unregistered tools, got: %s", result.Output)
	}

	result, err = man.Execute(context.Background(), json.RawMessage(`{"tool":"read"}`))
	if err != nil {
		t.Fatalf("man execute returned error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected read manual to be unavailable on clone, got %#v", result)
	}
}

func TestManToolRejectsUnknownTool(t *testing.T) {
	manager := NewManager(t.TempDir())
	man, ok := manager.Get("man")
	if !ok {
		t.Fatalf("expected man tool to be registered")
	}

	result, err := man.Execute(context.Background(), json.RawMessage(`{"tool":"missing"}`))
	if err != nil {
		t.Fatalf("man execute returned error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected unsuccessful result, got %#v", result)
	}
	if !strings.Contains(result.Error, "tool not found") {
		t.Fatalf("expected not found error, got: %s", result.Error)
	}
}
