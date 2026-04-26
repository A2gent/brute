package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	defaultFindFilesLimit    = 30 // Changed to 30 for pagination
	maxFindFilesLimit        = 2000
	defaultFindFilesPageSize = 30
)

// FindFilesTool finds files with include/exclude filters.
type FindFilesTool struct {
	workDir string
}

// FindFilesParams defines parameters for the find_files tool.
type FindFilesParams struct {
	Path       string   `json:"path,omitempty"`
	Pattern    string   `json:"pattern,omitempty"`
	Exclude    []string `json:"exclude,omitempty"`
	MaxResults int      `json:"max_results,omitempty"`
	Sort       string   `json:"sort,omitempty"`        // none|path|mtime
	Page       int      `json:"page,omitempty"`        // page number (1-based, default: 1)
	PageSize   int      `json:"page_size,omitempty"`   // items per page (default: 30)
	ShowHidden bool     `json:"show_hidden,omitempty"` // include hidden files/folders (default: false)
}

type fileSearchResult struct {
	path    string
	modTime int64
}

// NewFindFilesTool creates a new find_files tool.
func NewFindFilesTool(workDir string) *FindFilesTool {
	return &FindFilesTool{workDir: workDir}
}

func (t *FindFilesTool) Name() string {
	return "find_files"
}

func (t *FindFilesTool) Description() string {
	return `Find files with glob patterns and exclude filters.
Supports pagination (30 files per page by default) and hides hidden files by default.
Optimized for precise file discovery with compact output.
Use this before grep/read/edit to minimize context usage.`
}

func (t *FindFilesTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Base directory to search in (optional, defaults to working directory)",
			},
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Include glob pattern (default: '**/*')",
			},
			"exclude": map[string]interface{}{
				"type":        "array",
				"description": "Exclude glob patterns matched against relative paths",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of results (default: 30, max: 2000)",
			},
			"sort": map[string]interface{}{
				"type":        "string",
				"description": "Sort mode: none, path, or mtime (default: path)",
				"enum":        []string{"none", "path", "mtime"},
			},
			"page": map[string]interface{}{
				"type":        "integer",
				"description": "Page number for pagination (1-based, default: 1)",
				"minimum":     1,
			},
			"page_size": map[string]interface{}{
				"type":        "integer",
				"description": "Number of results per page (default: 30, max: 100)",
				"minimum":     1,
				"maximum":     100,
			},
			"show_hidden": map[string]interface{}{
				"type":        "boolean",
				"description": "Include hidden files and folders (default: false)",
			},
		},
	}
}

func (t *FindFilesTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p FindFilesParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	basePath := resolveToolPath(t.workDir, p.Path)

	pattern := p.Pattern
	if strings.TrimSpace(pattern) == "" {
		pattern = "**/*"
	}

	// Handle pagination parameters
	page := p.Page
	if page <= 0 {
		page = 1
	}

	pageSize := p.PageSize
	if pageSize <= 0 {
		pageSize = defaultFindFilesPageSize
	}
	if pageSize > 100 {
		pageSize = 100
	}

	// Handle max_results for backwards compatibility
	limit := p.MaxResults
	if limit <= 0 {
		limit = maxFindFilesLimit // Use max to collect all files for pagination
	}
	if limit > maxFindFilesLimit {
		limit = maxFindFilesLimit
	}

	sortMode := strings.ToLower(strings.TrimSpace(p.Sort))
	if sortMode == "" {
		sortMode = "path"
	}
	if sortMode != "none" && sortMode != "path" && sortMode != "mtime" {
		return &Result{Success: false, Error: "sort must be one of: none, path, mtime"}, nil
	}

	results, _, err := collectFileMatches(ctx, basePath, pattern, p.Exclude, p.ShowHidden, limit)
	if err != nil {
		return nil, err
	}

	sortFileResults(results, sortMode)

	if len(results) == 0 {
		return &Result{Success: true, Output: "No files found"}, nil
	}

	// Apply pagination
	totalResults := len(results)
	totalPages := (totalResults + pageSize - 1) / pageSize // Ceiling division

	if page > totalPages {
		return &Result{Success: true, Output: fmt.Sprintf("Page %d does not exist. Total pages: %d", page, totalPages)}, nil
	}

	startIdx := (page - 1) * pageSize
	endIdx := startIdx + pageSize
	if endIdx > totalResults {
		endIdx = totalResults
	}

	paginatedResults := results[startIdx:endIdx]

	lines := make([]string, 0, len(paginatedResults))
	for _, r := range paginatedResults {
		lines = append(lines, r.path)
	}

	output := strings.Join(lines, "\n")

	// Add pagination info
	if totalPages > 1 {
		output += fmt.Sprintf("\n\nPage %d of %d (showing %d-%d of %d files)",
			page, totalPages, startIdx+1, endIdx, totalResults)
		if page < totalPages {
			output += fmt.Sprintf("\nUse page=%d for next page", page+1)
		}
	} else if totalResults > 0 {
		output += fmt.Sprintf("\n\n(showing all %d files)", totalResults)
	}

	return &Result{Success: true, Output: output}, nil
}

func resolveToolPath(workDir, path string) string {
	if path == "" {
		return workDir
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(workDir, path)
}

func collectFileMatches(ctx context.Context, basePath, pattern string, exclude []string, showHidden bool, limit int) ([]fileSearchResult, int, error) {
	opts := []doublestar.GlobOption{doublestar.WithFilesOnly()}
	if !showHidden {
		opts = append(opts, doublestar.WithNoHidden())
	}

	matches, err := doublestar.FilepathGlob(filepath.Join(basePath, pattern), opts...)
	if err != nil {
		return nil, 0, fmt.Errorf("glob error: %w", err)
	}

	if limit <= 0 {
		limit = len(matches)
	}
	results := make([]fileSearchResult, 0, min(limit, len(matches)))
	totalIncluded := 0

	for _, match := range matches {
		if ctx.Err() != nil {
			return nil, totalIncluded, ctx.Err()
		}

		rel, err := filepath.Rel(basePath, match)
		if err != nil {
			rel = match
		}

		if isExcluded(rel, exclude) {
			continue
		}
		if !showHidden && isHiddenPath(rel) {
			continue
		}

		info, err := os.Stat(match)
		if err != nil || info.IsDir() {
			continue
		}

		totalIncluded++
		if len(results) < limit {
			results = append(results, fileSearchResult{path: rel, modTime: info.ModTime().UnixNano()})
		}
	}

	return results, totalIncluded, nil
}

func sortFileResults(results []fileSearchResult, sortMode string) {
	switch sortMode {
	case "path":
		sort.Slice(results, func(i, j int) bool {
			return results[i].path < results[j].path
		})
	case "mtime":
		sort.Slice(results, func(i, j int) bool {
			return results[i].modTime > results[j].modTime
		})
	}
}

func isExcluded(path string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		ok, err := doublestar.PathMatch(pattern, path)
		if err == nil && ok {
			return true
		}
		if err == nil {
			ok, _ = doublestar.PathMatch("**/"+pattern, path)
			if ok {
				return true
			}
		}
	}
	return false
}

// isHiddenPath checks if any component of the path is hidden (starts with '.')
func isHiddenPath(path string) bool {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for _, part := range parts {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Ensure FindFilesTool implements Tool.
var _ Tool = (*FindFilesTool)(nil)
