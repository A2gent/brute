package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// InsertLinesTool inserts lines at a specific position in a file.
type InsertLinesTool struct {
	workDir string
}

// InsertLinesParams defines parameters for the insert_lines tool.
type InsertLinesParams struct {
	Path      string `json:"path"`
	AfterLine int    `json:"after_line,omitempty"` // 0 = beginning, positive = after that line, omit/-1 = append
	Content   string `json:"content"`              // lines to insert
}

// NewInsertLinesTool creates a new insert_lines tool.
func NewInsertLinesTool(workDir string) *InsertLinesTool {
	return &InsertLinesTool{workDir: workDir}
}

func (t *InsertLinesTool) Name() string {
	return "insert_lines"
}

func (t *InsertLinesTool) Description() string {
	return `Insert lines at a specific position in a file.
Use this to add content without replacing existing lines.
Set after_line to 0 to insert at the beginning.
Omit after_line or set to -1 to append at the end.`
}

func (t *InsertLinesTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the file to edit",
			},
			"after_line": map[string]interface{}{
				"type":        "integer",
				"description": "Line number after which to insert (0 = beginning, omit/-1 = append)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Lines to insert (will be added after after_line)",
			},
		},
		"required": []string{"path", "content"},
	}
}

func (t *InsertLinesTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p InsertLinesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if p.Path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}

	path := resolveToolPath(t.workDir, p.Path)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Read file
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Result{Success: false, Error: fmt.Sprintf("file not found: %s", p.Path)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	lines, hadTrailingNewline := splitLines(string(content))
	insertLines, _ := splitLines(p.Content)

	// Determine insertion point
	insertAfter := p.AfterLine
	if insertAfter < 0 {
		// Append to end
		insertAfter = len(lines)
	}

	if insertAfter > len(lines) {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("after_line %d exceeds file length (%d lines)", insertAfter, len(lines)),
		}, nil
	}

	// Build new lines: [before] + [insert] + [after]
	newLines := make([]string, 0, len(lines)+len(insertLines))
	newLines = append(newLines, lines[:insertAfter]...)
	newLines = append(newLines, insertLines...)
	newLines = append(newLines, lines[insertAfter:]...)

	newContent := strings.Join(newLines, "\n")
	if hadTrailingNewline || len(newLines) > 0 {
		newContent += "\n"
	}

	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	var msg string
	if insertAfter == 0 {
		msg = fmt.Sprintf("Inserted %d line(s) at beginning of %s", len(insertLines), p.Path)
	} else if insertAfter == len(lines) {
		msg = fmt.Sprintf("Appended %d line(s) to end of %s", len(insertLines), p.Path)
	} else {
		msg = fmt.Sprintf("Inserted %d line(s) after line %d in %s", len(insertLines), insertAfter, p.Path)
	}

	return &Result{
		Success: true,
		Output:  msg,
	}, nil
}

// Ensure InsertLinesTool implements Tool.
var _ Tool = (*InsertLinesTool)(nil)
