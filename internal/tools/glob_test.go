package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGlobTool_Execute(t *testing.T) {
	tempDir := t.TempDir()

	createTestFile(t, tempDir, "old.go", "package old")
	time.Sleep(10 * time.Millisecond)
	createTestFile(t, tempDir, "src/new.go", "package new")
	createTestFile(t, tempDir, "src/readme.md", "# README")

	tool := NewGlobTool(tempDir)

	t.Run("finds files by recursive pattern", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"pattern": "**/*.go",
		})
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		assertSuccess(t, result)
		assertContains(t, result.Output, "old.go")
		assertContains(t, result.Output, "src/new.go")
		assertNotContains(t, result.Output, "readme.md")
	})

	t.Run("sorts by mtime newest first", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"pattern": "**/*.go",
		})
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		assertSuccess(t, result)
		lines := strings.Split(strings.TrimSpace(result.Output), "\n")
		if len(lines) < 2 {
			t.Fatalf("expected at least two results, got %q", result.Output)
		}
		if lines[0] != "src/new.go" {
			t.Fatalf("expected newest file first, got %q", lines[0])
		}
	})

	t.Run("empty results use legacy message", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"pattern": "*.missing",
		})
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		assertSuccess(t, result)
		if result.Output != "No files found matching pattern" {
			t.Fatalf("unexpected output: %q", result.Output)
		}
	})
}
