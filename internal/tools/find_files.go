package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

var errStopWalk = errors.New("stop walk")

var defaultPrunedDirs = map[string]struct{}{
	".cache":       {},
	".git":         {},
	".hg":          {},
	".next":        {},
	".nuxt":        {},
	".svn":         {},
	"build":        {},
	"coverage":     {},
	"dist":         {},
	"node_modules": {},
	"target":       {},
	"vendor":       {},
}

// FindFilesTool finds files with include/exclude filters.
type FindFilesTool struct {
	workDir string
}

// FindFilesParams defines parameters for the find_files tool.
type FindFilesParams struct {
	Path               string   `json:"path,omitempty"`
	Pattern            string   `json:"pattern,omitempty"`
	Exclude            []string `json:"exclude,omitempty"`
	MaxResults         int      `json:"max_results,omitempty"`
	Sort               string   `json:"sort,omitempty"`        // none|path|mtime
	Page               int      `json:"page,omitempty"`        // page number (1-based, default: 1)
	PageSize           int      `json:"page_size,omitempty"`   // items per page (default: 30)
	ShowHidden         bool     `json:"show_hidden,omitempty"` // include hidden files/folders (default: false)
	UseDefaultExcludes *bool    `json:"use_default_excludes,omitempty"`
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
			"use_default_excludes": map[string]interface{}{
				"type":        "boolean",
				"description": "Skip common heavy folders such as .git, node_modules, dist, build, and vendor (default: true)",
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

	sortMode := strings.ToLower(strings.TrimSpace(p.Sort))
	if sortMode == "" {
		sortMode = "path"
	}
	if sortMode != "none" && sortMode != "path" && sortMode != "mtime" {
		return &Result{Success: false, Error: "sort must be one of: none, path, mtime"}, nil
	}

	// Handle max_results for backwards compatibility
	limit := p.MaxResults
	if limit <= 0 {
		if sortMode == "none" {
			// If not sorting, we only need enough results to satisfy the requested page
			limit = page * pageSize
		} else {
			limit = maxFindFilesLimit // Use max to collect all files for sorting and pagination
		}
	}
	if limit > maxFindFilesLimit {
		limit = maxFindFilesLimit
	}

	useDefaultExcludes := true
	if p.UseDefaultExcludes != nil {
		useDefaultExcludes = *p.UseDefaultExcludes
	}

	results, _, err := collectFileMatches(ctx, basePath, pattern, p.Exclude, p.ShowHidden, useDefaultExcludes, limit)
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
	hasMore := limit > 0 && totalResults == limit && sortMode == "none"

	if totalPages > 1 || hasMore {
		displayedTotal := totalPages
		if hasMore {
			displayedTotal = totalPages + 1 // hint at least one more page
		}

		output += fmt.Sprintf("\n\nPage %d of %d+ (showing %d-%d of %d+ files)",
			page, displayedTotal, startIdx+1, endIdx, totalResults)
		if page < displayedTotal {
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
	primary := filepath.Join(workDir, path)
	if _, err := os.Stat(primary); err == nil {
		return primary
	}
	if nested := resolveNestedProjectPath(workDir, path); nested != "" {
		return nested
	}
	return primary
}

func resolveNestedProjectPath(workDir, relPath string) string {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return ""
	}
	matches := make([]string, 0, 1)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		root := filepath.Join(workDir, name)
		if info, err := os.Stat(filepath.Join(root, ".git")); err != nil || !info.IsDir() {
			continue
		}
		candidate := filepath.Join(root, relPath)
		if _, err := os.Stat(candidate); err == nil {
			matches = append(matches, candidate)
			continue
		}
		if parent := filepath.Dir(candidate); parent != "." && parent != candidate {
			if info, err := os.Stat(parent); err == nil && info.IsDir() {
				matches = append(matches, candidate)
			}
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func collectFileMatches(ctx context.Context, basePath, pattern string, exclude []string, showHidden bool, useDefaultExcludes bool, limit int) ([]fileSearchResult, int, error) {
	var results []fileSearchResult
	totalIncluded := 0

	err := filepath.WalkDir(basePath, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil // ignore access errors
		}

		rel, err := filepath.Rel(basePath, path)
		if err != nil {
			rel = filepath.Base(path)
		}

		if rel == "." {
			return nil
		}

		relSlash := filepath.ToSlash(rel)

		// Directory pruning
		if d.IsDir() {
			if useDefaultExcludes && isDefaultPrunedDir(relSlash) {
				return filepath.SkipDir
			}
			if !showHidden && isHiddenPath(relSlash) {
				return filepath.SkipDir
			}
			if isExcluded(relSlash, exclude) {
				return filepath.SkipDir
			}
			return nil
		}

		// File filtering
		if !showHidden && isHiddenPath(relSlash) {
			return nil
		}
		if isExcluded(relSlash, exclude) {
			return nil
		}

		// Pattern matching
		matched, _ := doublestar.PathMatch(pattern, relSlash)
		if !matched {
			return nil
		}

		totalIncluded++
		if limit <= 0 || len(results) < limit {
			info, err := d.Info()
			modTime := int64(0)
			if err == nil {
				modTime = info.ModTime().UnixNano()
			}
			results = append(results, fileSearchResult{path: relSlash, modTime: modTime})
		}

		if limit > 0 && len(results) >= limit {
			return errStopWalk
		}

		return nil
	})

	if errors.Is(err, errStopWalk) {
		return results, totalIncluded, nil
	}
	if err != nil {
		return results, totalIncluded, err
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

func isDefaultPrunedDir(path string) bool {
	name := filepath.Base(filepath.FromSlash(path))
	_, ok := defaultPrunedDirs[name]
	return ok
}

// isHiddenPath checks if any component of the path is hidden (starts with '.')
func isHiddenPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
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
