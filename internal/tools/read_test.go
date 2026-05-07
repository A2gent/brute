package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTool_LineNumberFormatting(t *testing.T) {
	workDir := t.TempDir()
	path := filepath.Join(workDir, "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatalf("failed to create sample file: %v", err)
	}

	tool := NewReadTool(workDir)

	t.Run("line numbers disabled by default", func(t *testing.T) {
		params := map[string]interface{}{
			"path":  "sample.txt",
			"limit": 2,
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if strings.Contains(result.Output, "\talpha") || strings.Contains(result.Output, "\tbeta") {
			t.Fatalf("expected output without line numbers, got: %q", result.Output)
		}
		if !strings.Contains(result.Output, "alpha\nbeta") {
			t.Fatalf("expected plain content lines, got: %q", result.Output)
		}
	})

	t.Run("line numbers can be enabled explicitly", func(t *testing.T) {
		params := map[string]interface{}{
			"path":                 "sample.txt",
			"limit":                2,
			"include_line_numbers": true,
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if !strings.Contains(result.Output, "     1\talpha") {
			t.Fatalf("expected line-numbered output for line 1, got: %q", result.Output)
		}
		if !strings.Contains(result.Output, "     2\tbeta") {
			t.Fatalf("expected line-numbered output for line 2, got: %q", result.Output)
		}
	})
}

func TestReadTool_ResolvesSingleNestedGitProject(t *testing.T) {
	workDir := t.TempDir()
	nestedRoot := filepath.Join(workDir, "spareto")
	if err := os.MkdirAll(filepath.Join(nestedRoot, ".git"), 0755); err != nil {
		t.Fatalf("failed to create nested git dir: %v", err)
	}
	path := filepath.Join(nestedRoot, "app/services/clickstack/metrics.rb")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create nested dirs: %v", err)
	}
	if err := os.WriteFile(path, []byte("nested metrics\n"), 0644); err != nil {
		t.Fatalf("failed to create nested file: %v", err)
	}

	tool := NewReadTool(workDir)
	raw, _ := json.Marshal(map[string]interface{}{
		"path": "app/services/clickstack/metrics.rb",
	})
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "nested metrics") {
		t.Fatalf("expected nested file content, got: %q", result.Output)
	}
}
