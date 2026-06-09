package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestFileSearchToolUsesIndexedFuzzySearch(t *testing.T) {
	tempDir := t.TempDir()
	createTestFile(t, tempDir, "src/components/SearchBox.tsx", "export function SearchBox() { return null }\n")
	createTestFile(t, tempDir, "node_modules/pkg/SearchBox.tsx", "dependency\n")

	tool := NewFileSearchTool(tempDir)
	result := executeSearchTool(t, tool, map[string]interface{}{"query": "searchbox"})

	assertSuccess(t, result)
	assertContains(t, result.Output, "src/components/SearchBox.tsx")
	assertNotContains(t, result.Output, "node_modules")
}

func TestContentSearchToolUsesIndexedContentSearch(t *testing.T) {
	tempDir := t.TempDir()
	createTestFile(t, tempDir, "src/app.ts", "export const indexedNeedle = true\n")

	tool := NewContentSearchTool(tempDir)
	result := executeSearchTool(t, tool, map[string]interface{}{"query": "indexedNeedle"})

	assertSuccess(t, result)
	assertContains(t, result.Output, "src/app.ts:1: export const indexedNeedle = true")
}

func executeSearchTool(t *testing.T, tool Tool, params map[string]interface{}) *Result {
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
