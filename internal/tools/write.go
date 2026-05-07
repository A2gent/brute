package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteTool creates or overwrites files
type WriteTool struct {
	workDir string
}

// WriteParams defines parameters for the write tool
type WriteParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// NewWriteTool creates a new write tool
func NewWriteTool(workDir string) *WriteTool {
	return &WriteTool{workDir: workDir}
}

func (t *WriteTool) Name() string {
	return "write"
}

func (t *WriteTool) Description() string {
	return `Create a new file or completely overwrite an existing file.
Use this when you need to create a new file or replace all contents.
For partial modifications, use the edit tool instead.`
}

func (t *WriteTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the file to write",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Content to write to the file",
			},
		},
		"required": []string{"path", "content"},
	}
}

func (t *WriteTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p WriteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if p.Path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	path := resolveToolPath(t.workDir, p.Path)

	// Create parent directories if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Check if file exists (for informational message)
	_, existErr := os.Stat(path)
	existed := existErr == nil

	// Write file
	if err := os.WriteFile(path, []byte(p.Content), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	action := "Created"
	if existed {
		action = "Overwrote"
	}

	return &Result{
		Success: true,
		Output:  fmt.Sprintf("%s %s (%d bytes)", action, p.Path, len(p.Content)),
	}, nil
}

// Ensure WriteTool implements Tool
var _ Tool = (*WriteTool)(nil)
