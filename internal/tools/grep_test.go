package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestGrepTool_DefaultExcludes(t *testing.T) {
	tempDir := t.TempDir()
	createTestFile(t, tempDir, "src/main.go", "package main\nconst needle = true\n")
	createTestFile(t, tempDir, "node_modules/pkg/index.js", "const needle = true\n")
	createTestFile(t, tempDir, "dist/bundle.js", "const needle = true\n")

	tool := NewGrepTool(tempDir)

	t.Run("skips heavy directories by default", func(t *testing.T) {
		result := executeGrepTool(t, tool, map[string]interface{}{
			"pattern": "needle",
			"mode":    "files",
		})

		assertSuccess(t, result)
		assertContains(t, result.Output, "src/main.go")
		assertNotContains(t, result.Output, "node_modules")
		assertNotContains(t, result.Output, "dist/bundle.js")
	})

	t.Run("can disable default excludes", func(t *testing.T) {
		result := executeGrepTool(t, tool, map[string]interface{}{
			"pattern":              "needle",
			"mode":                 "files",
			"use_default_excludes": false,
		})

		assertSuccess(t, result)
		assertContains(t, result.Output, "src/main.go")
		assertContains(t, result.Output, "node_modules/pkg/index.js")
		assertContains(t, result.Output, "dist/bundle.js")
	})
}

func TestGrepTool_LimitDoesNotReturnStopWalk(t *testing.T) {
	tempDir := t.TempDir()
	createTestFile(t, tempDir, "a.txt", "needle\n")
	createTestFile(t, tempDir, "b.txt", "needle\n")

	tool := NewGrepTool(tempDir)
	result := executeGrepTool(t, tool, map[string]interface{}{
		"pattern":     "needle",
		"max_results": 1,
	})

	assertSuccess(t, result)
	if strings.Contains(result.Output, "stop walk") {
		t.Fatalf("unexpected sentinel in output: %s", result.Output)
	}
}

func executeGrepTool(t *testing.T, tool *GrepTool, params map[string]interface{}) *Result {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("tool execution failed: %v", err)
	}
	return result
}
