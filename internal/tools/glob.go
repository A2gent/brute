package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const maxGlobResults = 1000

// GlobTool finds files by pattern
type GlobTool struct {
	workDir string
}

// GlobParams defines parameters for the glob tool
type GlobParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

// NewGlobTool creates a new glob tool
func NewGlobTool(workDir string) *GlobTool {
	return &GlobTool{workDir: workDir}
}

func (t *GlobTool) Name() string {
	return "glob"
}

func (t *GlobTool) Description() string {
	return `Find files by pattern matching using glob patterns.
Supports patterns like "**/*.go", "src/**/*.ts", "*.json".
Compatibility wrapper around find_files for simple pattern searches.
Returns matching file paths sorted by modification time (newest first).`
}

func (t *GlobTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern to match files (e.g., '**/*.go', 'src/**/*.ts')",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Base directory to search in (optional, defaults to working directory)",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GlobTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p GlobParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if p.Pattern == "" {
		return &Result{Success: false, Error: "pattern is required"}, nil
	}

	basePath := resolveToolPath(t.workDir, p.Path)
	files, total, err := collectFileMatches(ctx, basePath, p.Pattern, nil, true, maxGlobResults)
	if err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return &Result{
			Success: true,
			Output:  "No files found matching pattern",
		}, nil
	}

	sortFileResults(files, "mtime")

	// Build output
	var paths []string
	for _, f := range files {
		paths = append(paths, f.path)
	}

	output := strings.Join(paths, "\n")
	if total > maxGlobResults {
		output += fmt.Sprintf("\n\n(showing %d of %d matches)", maxGlobResults, total)
	}

	return &Result{
		Success: true,
		Output:  output,
	}, nil
}

// Ensure GlobTool implements Tool
var _ Tool = (*GlobTool)(nil)
