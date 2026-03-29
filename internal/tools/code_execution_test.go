package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeExecutionTool_Execute(t *testing.T) {
	tool := NewCodeExecutionTool(t.TempDir())
	if !tool.available {
		t.Skipf("python runtime unavailable: %s", tool.lookupError)
	}

	t.Run("returns output_data", func(t *testing.T) {
		params := map[string]interface{}{
			"code": `
nums = input_data.get("nums", [])
output_data = {"sum": sum(nums), "count": len(nums)}
`,
			"input": map[string]interface{}{
				"nums": []int{1, 2, 3, 4},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if !strings.Contains(result.Output, `"sum": 10`) {
			t.Fatalf("expected output to include sum, got: %s", result.Output)
		}
		if result.Metadata == nil || result.Metadata["python_version"] == nil {
			t.Fatalf("expected python_version metadata, got: %#v", result.Metadata)
		}
	})

	t.Run("blocks dangerous import", func(t *testing.T) {
		params := map[string]interface{}{
			"code": `import os
output_data = os.listdir(".")
`,
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure for dangerous import, got success: %s", result.Output)
		}
		if !strings.Contains(strings.ToLower(result.Error), "not allowed") {
			t.Fatalf("expected not allowed error, got: %s", result.Error)
		}
	})

	t.Run("allows open inside workspace", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(tool.workDir, "numbers.txt"), []byte("1\n2\n3\n"), 0o644); err != nil {
			t.Fatalf("failed to prepare fixture: %v", err)
		}
		params := map[string]interface{}{
			"code": `
with open("numbers.txt", "r", encoding="utf-8") as fh:
    output_data = [int(line.strip()) for line in fh if line.strip()]
`,
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if !strings.Contains(result.Output, "1") || !strings.Contains(result.Output, "3") {
			t.Fatalf("expected parsed content, got: %s", result.Output)
		}
	})

	t.Run("blocks open outside workspace", func(t *testing.T) {
		outsideDir := t.TempDir()
		outsideFile := filepath.Join(outsideDir, "secret.txt")
		if err := os.WriteFile(outsideFile, []byte("do-not-read"), 0o644); err != nil {
			t.Fatalf("failed to prepare outside fixture: %v", err)
		}
		params := map[string]interface{}{
			"code": `
with open(input_data["path"], "r", encoding="utf-8") as fh:
    output_data = fh.read()
`,
			"input": map[string]interface{}{
				"path": outsideFile,
			},
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure for outside file access, got success: %s", result.Output)
		}
		if !strings.Contains(strings.ToLower(result.Error), "outside the workspace") {
			t.Fatalf("expected workspace restriction error, got: %s", result.Error)
		}
	})
}
