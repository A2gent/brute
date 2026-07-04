package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/filesearch"
)

// FileSearchTool performs fast fuzzy file path/name search using the shared project index.
type FileSearchTool struct {
	workDir string
}

type FileSearchParams struct {
	Query      string `json:"query"`
	Path       string `json:"path,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

func invalidateIndexedSearch(workDir string) {
	if strings.TrimSpace(workDir) == "" {
		return
	}
	filesearch.DefaultManager().Invalidate(workDir)
}

func NewFileSearchTool(workDir string) *FileSearchTool {
	return &FileSearchTool{workDir: workDir}
}

func (t *FileSearchTool) Name() string {
	return "file_search"
}

func (t *FileSearchTool) Description() string {
	return `Fast indexed fuzzy search over file names and paths in the current project.
Prefer this over bash/find_files when you know part of a file name or path and need quick ranked results.
The per-project index skips dependency folders such as node_modules, vendor, dist, build, and .git.`
}

func (t *FileSearchTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "File name or path query. Supports fuzzy matching for small typos.",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Optional base directory to index/search. Defaults to the session working directory.",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of paths to return (default 20, max 500)",
			},
		},
		"required": []string{"query"},
	}
}

func (t *FileSearchTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p FileSearchParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if strings.TrimSpace(p.Query) == "" {
		return &Result{Success: false, Error: "query is required"}, nil
	}

	basePath := resolveToolPath(t.workDir, p.Path)
	result, err := filesearch.DefaultManager().Search(ctx, basePath, filesearch.SearchRequest{
		Query:          p.Query,
		FileLimit:      p.MaxResults,
		IncludeContent: false,
	})
	if err != nil {
		if errors.Is(err, filesearch.ErrIndexingDisabled) {
			return &Result{Success: false, Error: "file indexing is disabled; enable A2GENT_FILE_INDEXING_ENABLED in Caesar Tools settings, or use find_files for low-RAM filename search"}, nil
		}
		return nil, err
	}
	if len(result.FileMatches) == 0 {
		return &Result{Success: true, Output: "No files found"}, nil
	}

	lines := make([]string, 0, len(result.FileMatches))
	for _, match := range result.FileMatches {
		lines = append(lines, match.Path)
	}
	stats := result.Stats
	return &Result{
		Success: true,
		Output:  strings.Join(lines, "\n"),
		Metadata: map[string]interface{}{
			"indexed_files":     stats.IndexedFiles,
			"index_age_ms":      stats.IndexAgeMS,
			"build_duration_ms": stats.BuildDurationMS,
		},
	}, nil
}

// ContentSearchTool performs fast literal content search using the shared project index.
type ContentSearchTool struct {
	workDir string
}

type ContentSearchParams struct {
	Query      string `json:"query"`
	Path       string `json:"path,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

func NewContentSearchTool(workDir string) *ContentSearchTool {
	return &ContentSearchTool{workDir: workDir}
}

func (t *ContentSearchTool) Name() string {
	return "content_search"
}

func (t *ContentSearchTool) Description() string {
	return `Fast indexed literal substring search over project text file contents.
Prefer this over grep when you do not need regular expressions. Use grep for regex-specific searches.
The per-project index is memory-bounded and skips common dependency/build folders.`
}

func (t *ContentSearchTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Literal text to find in indexed file contents (case-insensitive)",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Optional base directory to index/search. Defaults to the session working directory.",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of matching lines to return (default 5, max 500)",
			},
		},
		"required": []string{"query"},
	}
}

func (t *ContentSearchTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p ContentSearchParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if strings.TrimSpace(p.Query) == "" {
		return &Result{Success: false, Error: "query is required"}, nil
	}

	basePath := resolveToolPath(t.workDir, p.Path)
	result, err := filesearch.DefaultManager().Search(ctx, basePath, filesearch.SearchRequest{
		Query:          p.Query,
		ContentLimit:   p.MaxResults,
		IncludeContent: true,
	})
	if err != nil {
		if errors.Is(err, filesearch.ErrIndexingDisabled) {
			return &Result{Success: false, Error: "file indexing is disabled; enable A2GENT_FILE_INDEXING_ENABLED in Caesar Tools settings, or use grep for low-RAM content search"}, nil
		}
		return nil, err
	}
	if len(result.ContentMatches) == 0 {
		return &Result{Success: true, Output: "No matches found"}, nil
	}

	lines := make([]string, 0, len(result.ContentMatches))
	for _, match := range result.ContentMatches {
		lines = append(lines, fmt.Sprintf("%s:%d: %s", match.Path, match.Line, match.Preview))
	}
	stats := result.Stats
	return &Result{
		Success: true,
		Output:  strings.Join(lines, "\n"),
		Metadata: map[string]interface{}{
			"indexed_files":         stats.IndexedFiles,
			"indexed_content_files": stats.IndexedContentFiles,
			"index_age_ms":          stats.IndexAgeMS,
			"build_duration_ms":     stats.BuildDurationMS,
		},
	}, nil
}

var _ Tool = (*FileSearchTool)(nil)
var _ Tool = (*ContentSearchTool)(nil)
