package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EditTool performs string replacements in files
type EditTool struct {
	workDir string
}

// EditParams defines parameters for the edit tool
type EditParams struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// NewEditTool creates a new edit tool
func NewEditTool(workDir string) *EditTool {
	return &EditTool{workDir: workDir}
}

func (t *EditTool) Name() string {
	return "edit"
}

func (t *EditTool) Description() string {
	return `Perform exact string replacements in files.
The old_string must match exactly (including whitespace and indentation).
By default, replaces only the first occurrence.
Set replace_all to true to replace all occurrences.
The edit will fail if old_string is not found or if it matches multiple times (without replace_all).`
}

func (t *EditTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the file to edit",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "The exact string to find and replace",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "The string to replace it with",
			},
			"replace_all": map[string]interface{}{
				"type":        "boolean",
				"description": "Replace all occurrences (default: false)",
			},
		},
		"required": []string{"path", "old_string", "new_string"},
	}
}

func (t *EditTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p EditParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if p.Path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}
	if p.OldString == "" {
		return &Result{Success: false, Error: "old_string is required"}, nil
	}
	if p.OldString == p.NewString {
		return &Result{Success: false, Error: "old_string and new_string must be different"}, nil
	}

	path := resolveToolPath(t.workDir, p.Path)

	// Read file
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Result{Success: false, Error: fmt.Sprintf("file not found: %s", p.Path)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	contentStr := string(content)

	// Count occurrences
	count := strings.Count(contentStr, p.OldString)

	if count == 0 {
		return &Result{Success: false, Error: "old_string not found in file"}, nil
	}

	if count > 1 && !p.ReplaceAll {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("old_string found %d times - provide more context to match uniquely, or set replace_all to true", count),
		}, nil
	}

	// Perform replacement
	var newContent string
	if p.ReplaceAll {
		newContent = strings.ReplaceAll(contentStr, p.OldString, p.NewString)
	} else {
		newContent = strings.Replace(contentStr, p.OldString, p.NewString, 1)
	}

	// Write file
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	if p.ReplaceAll && count > 1 {
		return &Result{
			Success: true,
			Output:  fmt.Sprintf("Replaced %d occurrences in %s", count, p.Path),
		}, nil
	}

	return &Result{
		Success: true,
		Output:  fmt.Sprintf("Edited %s", p.Path),
	}, nil
}

// Ensure EditTool implements Tool
var _ Tool = (*EditTool)(nil)
