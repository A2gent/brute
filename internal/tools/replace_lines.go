package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ReplaceLinesTool replaces an exact line range in a file.
type ReplaceLinesTool struct {
	workDir string
}

// ReplaceLinesParams defines parameters for the replace_lines tool.
type ReplaceLinesParams struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"` // 1-based inclusive
	EndLine   int    `json:"end_line"`   // 1-based inclusive
	Content   string `json:"content"`    // replacement content (may be empty for deletion)
}

// NewReplaceLinesTool creates a new replace_lines tool.
func NewReplaceLinesTool(workDir string) *ReplaceLinesTool {
	return &ReplaceLinesTool{workDir: workDir}
}

func (t *ReplaceLinesTool) Name() string {
	return "replace_lines"
}

func (t *ReplaceLinesTool) Description() string {
	return `Replace a specific line range in a file.
Use this for precise edits when you know the line numbers.
This avoids sending large old_string payloads and reduces context usage.`
}

func (t *ReplaceLinesTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the file to edit",
			},
			"start_line": map[string]interface{}{
				"type":        "integer",
				"description": "1-based start line (inclusive)",
			},
			"end_line": map[string]interface{}{
				"type":        "integer",
				"description": "1-based end line (inclusive)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Replacement text for the line range",
			},
		},
		"required": []string{"path", "start_line", "end_line", "content"},
	}
}

func (t *ReplaceLinesTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p ReplaceLinesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if p.Path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}
	if p.StartLine <= 0 || p.EndLine <= 0 {
		return &Result{Success: false, Error: "start_line and end_line must be >= 1"}, nil
	}
	if p.StartLine > p.EndLine {
		return &Result{Success: false, Error: "start_line must be <= end_line"}, nil
	}

	path := resolveToolPath(t.workDir, p.Path)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Result{Success: false, Error: fmt.Sprintf("file not found: %s", p.Path)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	lines, hadTrailingNewline := splitLines(string(content))
	if p.EndLine > len(lines) {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("line range %d-%d exceeds file length (%d lines)", p.StartLine, p.EndLine, len(lines)),
		}, nil
	}

	replacement, replacementTrailingNewline := splitLines(p.Content)

	newLines := make([]string, 0, len(lines)-(p.EndLine-p.StartLine+1)+len(replacement))
	newLines = append(newLines, lines[:p.StartLine-1]...)
	newLines = append(newLines, replacement...)
	newLines = append(newLines, lines[p.EndLine:]...)

	newContent := strings.Join(newLines, "\n")
	if shouldKeepTrailingNewline(hadTrailingNewline, replacementTrailingNewline, len(newLines)) {
		newContent += "\n"
	}

	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return &Result{
		Success: true,
		Output:  fmt.Sprintf("Replaced lines %d-%d in %s", p.StartLine, p.EndLine, p.Path),
	}, nil
}

func splitLines(s string) ([]string, bool) {
	if s == "" {
		return []string{}, false
	}
	hadTrailingNewline := strings.HasSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n"), hadTrailingNewline
}

func shouldKeepTrailingNewline(originalTrailing, replacementTrailing bool, lineCount int) bool {
	if replacementTrailing {
		return true
	}
	if originalTrailing && lineCount > 0 {
		return true
	}
	return false
}

// Ensure ReplaceLinesTool implements Tool.
var _ Tool = (*ReplaceLinesTool)(nil)
