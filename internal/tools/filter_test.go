package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilterTool_Execute(t *testing.T) {
	workDir := t.TempDir()
	tool := NewFilterTool(workDir)

	t.Run("filter input by contains case-insensitive", func(t *testing.T) {
		params := map[string]interface{}{
			"input":    "Alpha\nbeta\nGamma\nbeta two\n",
			"contains": "BETA",
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if strings.TrimSpace(result.Output) != "beta\nbeta two" {
			t.Fatalf("unexpected output: %q", result.Output)
		}
	})

	t.Run("regex and not_regex", func(t *testing.T) {
		params := map[string]interface{}{
			"input":     "v1 ok\nv2 skip\nv3 ok\n",
			"regex":     `^v\d`,
			"not_regex": `skip`,
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if strings.TrimSpace(result.Output) != "v1 ok\nv3 ok" {
			t.Fatalf("unexpected output: %q", result.Output)
		}
	})

	t.Run("range and max_lines", func(t *testing.T) {
		params := map[string]interface{}{
			"input":      "1\n2\n3\n4\n5\n",
			"start_line": 2,
			"end_line":   5,
			"max_lines":  2,
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if strings.TrimSpace(result.Output) != "2\n3" {
			t.Fatalf("unexpected output: %q", result.Output)
		}
		if result.Metadata == nil || result.Metadata["truncated_by_max_lines"] != true {
			t.Fatalf("expected truncated_by_max_lines metadata, got: %#v", result.Metadata)
		}
	})

	t.Run("path input", func(t *testing.T) {
		path := filepath.Join(workDir, "sample.txt")
		if err := os.WriteFile(path, []byte("aa\nbb\ncc\n"), 0644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}
		params := map[string]interface{}{
			"path":     "sample.txt",
			"contains": "b",
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if strings.TrimSpace(result.Output) != "bb" {
			t.Fatalf("unexpected output: %q", result.Output)
		}
	})

	t.Run("reject both input and path", func(t *testing.T) {
		params := map[string]interface{}{
			"input": "x",
			"path":  "sample.txt",
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
		if !strings.Contains(result.Error, "either input or path") && !strings.Contains(result.Error, "provide either input or path") {
			t.Fatalf("unexpected error: %s", result.Error)
		}
	})
}
