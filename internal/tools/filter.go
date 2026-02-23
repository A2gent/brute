package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	filterDefaultMaxLines    = 200
	filterHardMaxLines       = 5000
	filterDefaultMaxOutChars = 12000
	filterHardMaxOutChars    = 200000
	filterMaxInputBytes      = 2 * 1024 * 1024
)

// FilterTool filters text input (or file content) to reduce context size.
type FilterTool struct {
	workDir string
}

type FilterParams struct {
	Input          string `json:"input,omitempty"`
	Path           string `json:"path,omitempty"`
	Contains       string `json:"contains,omitempty"`
	NotContains    string `json:"not_contains,omitempty"`
	Regex          string `json:"regex,omitempty"`
	NotRegex       string `json:"not_regex,omitempty"`
	CaseSensitive  bool   `json:"case_sensitive,omitempty"`
	IncludeEmpty   bool   `json:"include_empty,omitempty"`
	StartLine      int    `json:"start_line,omitempty"` // 1-based, after filtering
	EndLine        int    `json:"end_line,omitempty"`   // 1-based, after filtering
	MaxLines       int    `json:"max_lines,omitempty"`
	MaxOutputChars int    `json:"max_output_chars,omitempty"`
}

func NewFilterTool(workDir string) *FilterTool {
	return &FilterTool{workDir: workDir}
}

func (t *FilterTool) Name() string {
	return "filter"
}

func (t *FilterTool) Description() string {
	return `Filter text by contains/regex rules.
Input can be provided directly or loaded from a file path.
Useful in pipelines (e.g. fetch_url -> filter, read -> filter) to reduce LLM context.`
}

func (t *FilterTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{
				"type":        "string",
				"description": "Text to filter directly. Use this in tool pipelines.",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Optional file path to load and filter when input is not provided.",
			},
			"contains": map[string]interface{}{
				"type":        "string",
				"description": "Keep lines that contain this substring.",
			},
			"not_contains": map[string]interface{}{
				"type":        "string",
				"description": "Drop lines that contain this substring.",
			},
			"regex": map[string]interface{}{
				"type":        "string",
				"description": "Keep lines matching this Go regex pattern.",
			},
			"not_regex": map[string]interface{}{
				"type":        "string",
				"description": "Drop lines matching this Go regex pattern.",
			},
			"case_sensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "Controls case sensitivity for contains/not_contains checks (default: false).",
			},
			"include_empty": map[string]interface{}{
				"type":        "boolean",
				"description": "Keep empty lines (default: false).",
			},
			"start_line": map[string]interface{}{
				"type":        "integer",
				"description": "1-based start line after filtering (inclusive).",
			},
			"end_line": map[string]interface{}{
				"type":        "integer",
				"description": "1-based end line after filtering (inclusive).",
			},
			"max_lines": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum lines to return after filtering (default: 200, max: 5000).",
			},
			"max_output_chars": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum output characters (default: 12000, max: 200000).",
			},
		},
	}
}

func (t *FilterTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p FilterParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if strings.TrimSpace(p.Input) == "" && strings.TrimSpace(p.Path) == "" {
		return &Result{Success: false, Error: "either input or path is required"}, nil
	}
	if strings.TrimSpace(p.Input) != "" && strings.TrimSpace(p.Path) != "" {
		return &Result{Success: false, Error: "provide either input or path, not both"}, nil
	}
	if p.StartLine < 0 || p.EndLine < 0 {
		return &Result{Success: false, Error: "start_line and end_line must be >= 1 when provided"}, nil
	}
	if p.StartLine > 0 && p.EndLine > 0 && p.StartLine > p.EndLine {
		return &Result{Success: false, Error: "start_line must be <= end_line"}, nil
	}

	input, source, err := t.loadInput(p)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	var keepRe *regexp.Regexp
	var dropRe *regexp.Regexp
	if strings.TrimSpace(p.Regex) != "" {
		keepRe, err = regexp.Compile(p.Regex)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("invalid regex: %v", err)}, nil
		}
	}
	if strings.TrimSpace(p.NotRegex) != "" {
		dropRe, err = regexp.Compile(p.NotRegex)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("invalid not_regex: %v", err)}, nil
		}
	}

	maxLines := p.MaxLines
	if maxLines <= 0 {
		maxLines = filterDefaultMaxLines
	}
	if maxLines > filterHardMaxLines {
		maxLines = filterHardMaxLines
	}
	maxOut := p.MaxOutputChars
	if maxOut <= 0 {
		maxOut = filterDefaultMaxOutChars
	}
	if maxOut > filterHardMaxOutChars {
		maxOut = filterHardMaxOutChars
	}

	lines := strings.Split(input, "\n")
	filtered := make([]string, 0, len(lines))
	includeEmpty := p.IncludeEmpty
	containsNeedle := p.Contains
	notContainsNeedle := p.NotContains
	if !p.CaseSensitive {
		containsNeedle = strings.ToLower(containsNeedle)
		notContainsNeedle = strings.ToLower(notContainsNeedle)
	}

	for _, line := range lines {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !includeEmpty && strings.TrimSpace(line) == "" {
			continue
		}

		lineForContains := line
		if !p.CaseSensitive {
			lineForContains = strings.ToLower(lineForContains)
		}

		if containsNeedle != "" && !strings.Contains(lineForContains, containsNeedle) {
			continue
		}
		if notContainsNeedle != "" && strings.Contains(lineForContains, notContainsNeedle) {
			continue
		}
		if keepRe != nil && !keepRe.MatchString(line) {
			continue
		}
		if dropRe != nil && dropRe.MatchString(line) {
			continue
		}
		filtered = append(filtered, line)
	}

	start := 1
	if p.StartLine > 0 {
		start = p.StartLine
	}
	end := len(filtered)
	if p.EndLine > 0 && p.EndLine < end {
		end = p.EndLine
	}

	window := make([]string, 0)
	if start <= len(filtered) && start <= end {
		window = filtered[start-1 : end]
	}
	truncatedByLines := false
	if len(window) > maxLines {
		window = window[:maxLines]
		truncatedByLines = true
	}

	out := strings.Join(window, "\n")
	out, truncatedByChars := truncateWithFlag(out, maxOut)

	meta := map[string]interface{}{
		"source":                 source,
		"input_lines":            len(lines),
		"matched_lines":          len(filtered),
		"returned_lines":         len(window),
		"applied_start_line":     start,
		"applied_end_line":       end,
		"truncated_by_max_lines": truncatedByLines,
		"truncated_by_max_chars": truncatedByChars,
	}
	if out == "" {
		out = "(no lines matched filter)"
	}

	return &Result{
		Success:  true,
		Output:   out,
		Metadata: meta,
	}, nil
}

func (t *FilterTool) loadInput(p FilterParams) (string, string, error) {
	if strings.TrimSpace(p.Input) != "" {
		return p.Input, "input", nil
	}

	path := p.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.workDir, path)
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "", "", fmt.Errorf("file not found: %s", p.Path)
	}
	if err != nil {
		return "", "", fmt.Errorf("failed to stat file: %v", err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("%s is a directory", p.Path)
	}
	if info.Size() > filterMaxInputBytes {
		return "", "", fmt.Errorf("file too large for filter tool (%d > %d bytes)", info.Size(), filterMaxInputBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("failed to read file: %v", err)
	}
	return string(data), "path", nil
}

// Ensure FilterTool implements Tool.
var _ Tool = (*FilterTool)(nil)
