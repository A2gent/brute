package tools

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultReadLimit = 20
	maxLineLength    = 2000
)

// ReadTool reads file contents
type ReadTool struct {
	workDir string
	cache   readSessionCache
}

// ReadParams defines parameters for the read tool
type ReadParams struct {
	Path               string `json:"path"`
	Force              bool   `json:"force,omitempty"`
	Offset             int    `json:"offset,omitempty"`               // 0-based line offset
	Limit              int    `json:"limit,omitempty"`                // Number of lines to read
	StartLine          int    `json:"start_line,omitempty"`           // 1-based inclusive
	EndLine            int    `json:"end_line,omitempty"`             // 1-based inclusive
	IncludeLineNumbers bool   `json:"include_line_numbers,omitempty"` // Default false
}

// NewReadTool creates a new read tool
func NewReadTool(workDir string) *ReadTool {
	return &ReadTool{workDir: workDir}
}

func (t *ReadTool) Name() string {
	return "read"
}

func (t *ReadTool) Description() string {
	return `Read file contents from the filesystem.
By default reads up to 20 lines from the beginning.
Use offset and limit for reading specific sections of large files.
Use start_line and end_line for exact 1-based range reads.
Set include_line_numbers=true to prefix each line with its 1-based line number.
Unchanged repeat reads return a reference to an earlier full result still in context.
Set force=true to always return the full body.`
}

func (t *ReadTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"force": map[string]interface{}{
				"type":        "boolean",
				"description": "Always return the full body, even if an unchanged earlier read is still in this session context",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Absolute or relative path to the file",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Line number to start reading from (0-based, optional)",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of lines to read (default: 2000)",
			},
			"start_line": map[string]interface{}{
				"type":        "integer",
				"description": "1-based start line for exact range read (inclusive, optional)",
			},
			"end_line": map[string]interface{}{
				"type":        "integer",
				"description": "1-based end line for exact range read (inclusive, optional)",
			},
			"include_line_numbers": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to include 1-based line numbers in output (default: false)",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p ReadParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if p.Path == "" {
		return &Result{Success: false, Error: "path is required"}, nil
	}
	if p.StartLine < 0 || p.EndLine < 0 {
		return &Result{Success: false, Error: "start_line and end_line must be >= 1 when provided"}, nil
	}
	if p.StartLine > 0 && p.EndLine > 0 && p.StartLine > p.EndLine {
		return &Result{Success: false, Error: "start_line must be <= end_line"}, nil
	}

	path := filepath.Clean(resolveToolPath(t.workDir, p.Path))

	// Check if file exists
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return &Result{Success: false, Error: fmt.Sprintf("file not found: %s", p.Path)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	if info.IsDir() {
		return &Result{Success: false, Error: fmt.Sprintf("%s is a directory", p.Path)}, nil
	}

	if !info.Mode().IsRegular() {
		return &Result{Success: false, Error: fmt.Sprintf("%s is not a regular file", p.Path)}, nil
	}

	// Open file
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Set defaults
	offset := p.Offset
	limit := p.Limit
	if limit <= 0 {
		limit = defaultReadLimit
	}
	rangeMode := p.StartLine > 0 || p.EndLine > 0

	start, end := offset+1, offset+limit
	if offset < 0 {
		start, end = 1, limit
	}
	if rangeMode {
		start = p.StartLine
		if start <= 0 {
			start = 1
		}
		end = p.EndLine
		if end <= 0 {
			end = start + defaultReadLimit - 1
		}
	}
	key := readCacheKey{path: path, start: start, end: end, lineNumbers: p.IncludeLineNumbers}

	// Hash the entire file, including bytes outside the requested range. The
	// scanner and hasher share one stream so the returned body matches the hash.
	hasher := sha256.New()
	reader := io.TeeReader(file, hasher)
	// Read lines
	var lines []string
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNum := 0
	linesRead := 0

	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		lineNum++

		if rangeMode {
			startLine := p.StartLine
			if startLine <= 0 {
				startLine = 1
			}
			endLine := p.EndLine
			if endLine <= 0 {
				endLine = startLine + defaultReadLimit - 1
			}

			if lineNum < startLine {
				continue
			}
			if lineNum > endLine {
				break
			}
		}

		// Skip lines before offset
		if !rangeMode && lineNum <= offset {
			continue
		}

		// Check limit
		if !rangeMode && linesRead >= limit {
			break
		}

		line := scanner.Text()

		// Truncate long lines
		if len(line) > maxLineLength {
			line = line[:maxLineLength] + "..."
		}

		if p.IncludeLineNumbers {
			// Format with line number (cat -n style)
			lines = append(lines, fmt.Sprintf("%6d\t%s", lineNum, line))
		} else {
			lines = append(lines, line)
		}
		linesRead++
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			_, _ = hasher.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error hashing file: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var hash [sha256.Size]byte
	copy(hash[:], hasher.Sum(nil))
	if !p.Force {
		if cached := t.cachedRead(readCacheSession(ctx), key, hash); cached != nil {
			return cached, nil
		}
	}

	if len(lines) == 0 {
		t.rememberRead(ctx, key, hash, "(empty file or no lines in range)", 0)
		return &Result{
			Success: true,
			Output:  "(empty file or no lines in range)",
		}, nil
	}

	output := strings.Join(lines, "\n")
	if !rangeMode && linesRead == limit {
		output += fmt.Sprintf("\n\n(showing lines %d-%d, file may have more content)", offset+1, lineNum)
	}
	if rangeMode && p.StartLine > 0 {
		endLine := p.EndLine
		if endLine <= 0 {
			endLine = p.StartLine + linesRead - 1
		}
		output += fmt.Sprintf("\n\n(showing requested range starting at line %d through %d)", p.StartLine, endLine)
	}

	t.rememberRead(ctx, key, hash, output, linesRead)
	return &Result{
		Success: true,
		Output:  output,
	}, nil
}

// Ensure ReadTool implements Tool
var _ Tool = (*ReadTool)(nil)
