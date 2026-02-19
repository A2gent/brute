package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindFilesTool_Execute(t *testing.T) {
	tempDir := t.TempDir()

	createTestFile(t, tempDir, "readme.md", "# README")
	createTestFile(t, tempDir, "docs/guide.md", "# Guide")
	createTestFile(t, tempDir, "src/main.go", "package main")
	createTestFile(t, tempDir, "src/utils.go", "package utils")
	createTestFile(t, tempDir, ".hidden/config.yml", "secret: value")

	tool := NewFindFilesTool(tempDir)

	t.Run("find all markdown files", func(t *testing.T) {
		params := map[string]interface{}{
			"pattern": "*.md",
		}
		result := executeTool(t, tool, params)

		assertSuccess(t, result)
		assertContains(t, result.Output, "readme.md")
		assertNotContains(t, result.Output, "main.go")
	})

	t.Run("find go files recursively", func(t *testing.T) {
		params := map[string]interface{}{
			"pattern": "**/*.go",
		}
		result := executeTool(t, tool, params)

		assertSuccess(t, result)
		assertContains(t, result.Output, "main.go")
		assertContains(t, result.Output, "utils.go")
		assertNotContains(t, result.Output, "readme.md")
	})

	t.Run("exclude patterns", func(t *testing.T) {
		params := map[string]interface{}{
			"pattern": "**/*",
			"exclude": []string{"*.go"},
		}
		result := executeTool(t, tool, params)

		assertSuccess(t, result)
		assertContains(t, result.Output, "readme.md")
		assertNotContains(t, result.Output, "main.go")
	})

	t.Run("hidden files excluded by default", func(t *testing.T) {
		params := map[string]interface{}{
			"pattern": "**/*",
		}
		result := executeTool(t, tool, params)

		assertSuccess(t, result)
		assertNotContains(t, result.Output, ".hidden")
		assertNotContains(t, result.Output, "config.yml")
	})

	t.Run("show hidden files when requested", func(t *testing.T) {
		params := map[string]interface{}{
			"pattern":     "**/*",
			"show_hidden": true,
		}
		result := executeTool(t, tool, params)

		assertSuccess(t, result)
		assertContains(t, result.Output, ".hidden")
		assertContains(t, result.Output, "config.yml")
	})
}

func TestFindFilesTool_Pagination(t *testing.T) {
	tempDir := t.TempDir()

	for i := 0; i < 10; i++ {
		name := filepath.Join("files", string('a'+byte(i))+".txt")
		createTestFile(t, tempDir, name, "content")
	}

	tool := NewFindFilesTool(tempDir)

	t.Run("paginate results", func(t *testing.T) {
		params := map[string]interface{}{
			"pattern":   "files/*.txt",
			"page":      1,
			"page_size": 3,
		}
		result := executeTool(t, tool, params)

		assertSuccess(t, result)
		assertContains(t, result.Output, "Page 1 of 4")
		assertContains(t, result.Output, "Use page=2 for next page")
	})

	t.Run("second page", func(t *testing.T) {
		params := map[string]interface{}{
			"pattern":   "files/*.txt",
			"page":      2,
			"page_size": 3,
		}
		result := executeTool(t, tool, params)

		assertSuccess(t, result)
		assertContains(t, result.Output, "Page 2 of 4")
	})

	t.Run("page beyond range", func(t *testing.T) {
		params := map[string]interface{}{
			"pattern":   "files/*.txt",
			"page":      99,
			"page_size": 3,
		}
		result := executeTool(t, tool, params)

		assertSuccess(t, result)
		assertContains(t, result.Output, "Page 99 does not exist")
	})
}

func TestFindFilesTool_Sorting(t *testing.T) {
	tempDir := t.TempDir()

	createTestFile(t, tempDir, "zebra.txt", "z")
	createTestFile(t, tempDir, "alpha.txt", "a")
	createTestFile(t, tempDir, "mike.txt", "m")

	tool := NewFindFilesTool(tempDir)

	t.Run("sort by path", func(t *testing.T) {
		params := map[string]interface{}{
			"pattern": "*.txt",
			"sort":    "path",
		}
		result := executeTool(t, tool, params)

		assertSuccess(t, result)
		lines := strings.Split(strings.TrimSpace(result.Output), "\n")
		if len(lines) >= 3 {
			if !strings.Contains(lines[0], "alpha.txt") {
				t.Errorf("expected alpha.txt first when sorted by path, got: %s", lines[0])
			}
		}
	})
}

func TestFindFilesTool_InvalidParams(t *testing.T) {
	tempDir := t.TempDir()
	tool := NewFindFilesTool(tempDir)

	t.Run("invalid sort parameter", func(t *testing.T) {
		params := map[string]interface{}{
			"pattern": "*",
			"sort":    "invalid",
		}
		result := executeTool(t, tool, params)

		if result.Success {
			t.Error("expected failure for invalid sort parameter")
		}
		assertContains(t, result.Error, "sort must be one of")
	})
}

func TestFindFilesTool_EmptyResults(t *testing.T) {
	tempDir := t.TempDir()
	tool := NewFindFilesTool(tempDir)

	t.Run("no matching files", func(t *testing.T) {
		params := map[string]interface{}{
			"pattern": "*.nonexistent",
		}
		result := executeTool(t, tool, params)

		assertSuccess(t, result)
		assertContains(t, result.Output, "No files found")
	})
}

func createTestFile(t *testing.T, baseDir, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(baseDir, relPath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create directory %s: %v", dir, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create file %s: %v", fullPath, err)
	}
}

func executeTool(t *testing.T, tool *FindFilesTool, params map[string]interface{}) *Result {
	t.Helper()
	jsonParams, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}
	result, err := tool.Execute(context.Background(), jsonParams)
	if err != nil {
		t.Fatalf("tool execution failed: %v", err)
	}
	return result
}

func assertSuccess(t *testing.T, result *Result) {
	t.Helper()
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}

func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Errorf("expected %q to NOT contain %q", s, substr)
	}
}
