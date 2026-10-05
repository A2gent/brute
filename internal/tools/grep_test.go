package tools

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestGrepTool_SummaryThreshold(t *testing.T) {
	for _, n := range []int{100, 101} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			dir := t.TempDir()
			createTestFile(t, dir, "a.txt", strings.Repeat("needle\n", n))
			tool := NewGrepTool(dir)
			result := executeGrepTool(t, tool, map[string]interface{}{"pattern": "needle"})
			summary := strings.Contains(result.Output, "full_output=true")
			if summary != (n > 100) {
				t.Fatalf("summary=%v for %d rows", summary, n)
			}
			if summary {
				assertContains(t, result.Output, "a.txt: 101")
				assertContains(t, result.Output, "a.txt:10:")
				assertNotContains(t, result.Output, "a.txt:11:")
			}
			full := executeGrepTool(t, tool, map[string]interface{}{"pattern": "needle", "full_output": true})
			assertContains(t, full.Output, fmt.Sprintf("a.txt:%d:", n))
		})
	}
}

func TestGrepTool_TokenThresholdAndCounts(t *testing.T) {
	dir := t.TempDir()
	createTestFile(t, dir, "a.txt", strings.Repeat("needle"+strings.Repeat("x", 450)+"\n", 40))
	createTestFile(t, dir, "b.txt", "needle\n")
	tool := NewGrepTool(dir)
	result := executeGrepTool(t, tool, map[string]interface{}{"pattern": "needle"})
	assertContains(t, result.Output, "full_output=true")
	assertContains(t, result.Output, "a.txt: 40")
	assertContains(t, result.Output, "b.txt: 1")
	counts := executeGrepTool(t, tool, map[string]interface{}{"pattern": "needle", "mode": "count", "max_results": 1})
	assertContains(t, counts.Output, "a.txt: 40")
	assertContains(t, counts.Output, "b.txt: 1")
}
