package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	maxGrepResults    = 500
	maxGrepLineLength = 500
)

// GrepTool searches file contents using regex
type GrepTool struct {
	workDir string
}

// GrepParams defines parameters for the grep tool
type GrepParams struct {
	Pattern            string   `json:"pattern"`
	Path               string   `json:"path,omitempty"`
	Include            string   `json:"include,omitempty"` // File pattern filter
	Exclude            []string `json:"exclude,omitempty"` // Relative path filters
	MaxResults         int      `json:"max_results,omitempty"`
	MaxMatchesPerFile  int      `json:"max_matches_per_file,omitempty"`
	Mode               string   `json:"mode,omitempty"` // lines|files|count
	UseDefaultExcludes *bool    `json:"use_default_excludes,omitempty"`
}

// NewGrepTool creates a new grep tool
func NewGrepTool(workDir string) *GrepTool {
	return &GrepTool{workDir: workDir}
}

func (t *GrepTool) Name() string {
	return "grep"
}

func (t *GrepTool) Description() string {
	return `Search file contents using regular expressions.
Use mode=files or mode=count for compact outputs.
Use include/exclude and limits to reduce context usage.`
}

func (t *GrepTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Regular expression pattern to search for",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory to search in (optional, defaults to working directory)",
			},
			"include": map[string]interface{}{
				"type":        "string",
				"description": "File pattern to include (e.g., '*.go', '*.{ts,tsx}')",
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
				"description": "Maximum output rows (default: 500)",
			},
			"max_matches_per_file": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum matches to emit per file (default: unlimited)",
			},
			"mode": map[string]interface{}{
				"type":        "string",
				"description": "Output mode: lines (default), files, count",
				"enum":        []string{"lines", "files", "count"},
			},
			"use_default_excludes": map[string]interface{}{
				"type":        "boolean",
				"description": "Skip common heavy folders such as .git, node_modules, dist, build, and vendor (default: true)",
			},
		},
		"required": []string{"pattern"},
	}
}

type grepMatch struct {
	file    string
	line    int
	content string
	modTime int64
}

func (t *GrepTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p GrepParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if p.Pattern == "" {
		return &Result{Success: false, Error: "pattern is required"}, nil
	}
	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	if mode == "" {
		mode = "lines"
	}
	if mode != "lines" && mode != "files" && mode != "count" {
		return &Result{Success: false, Error: "mode must be one of: lines, files, count"}, nil
	}

	// Compile regex
	re, err := regexp.Compile(p.Pattern)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("invalid regex: %v", err)}, nil
	}

	// Determine base path
	basePath := t.workDir
	if p.Path != "" {
		basePath = resolveToolPath(t.workDir, p.Path)
	}

	// Determine file pattern
	filePattern := "**/*"
	if p.Include != "" {
		filePattern = "**/" + p.Include
	}

	// Search files
	var matches []grepMatch
	fileCounts := make(map[string]int)
	var mu sync.Mutex

	maxResults := p.MaxResults
	if maxResults <= 0 {
		maxResults = maxGrepResults
	}
	if maxResults > maxGrepResults {
		maxResults = maxGrepResults
	}
	maxPerFile := p.MaxMatchesPerFile
	useDefaultExcludes := true
	if p.UseDefaultExcludes != nil {
		useDefaultExcludes = *p.UseDefaultExcludes
	}

	type grepTask struct {
		path    string
		relPath string
		modTime int64
	}

	taskChan := make(chan grepTask, 100)
	var wg sync.WaitGroup

	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	numWorkers := runtime.NumCPU()
	if numWorkers < 2 {
		numWorkers = 2
	} else if numWorkers > 16 {
		numWorkers = 16
	}

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskChan {
				if workerCtx.Err() != nil {
					continue
				}

				fileMatches, totalCount := t.searchFile(task.path, task.relPath, re, task.modTime, maxPerFile, mode == "files")

				if totalCount > 0 {
					mu.Lock()
					fileCounts[task.relPath] = totalCount
					matches = append(matches, fileMatches...)
					if len(matches) >= maxResults {
						workerCancel()
					}
					mu.Unlock()
				}
			}
		}()
	}

	err = filepath.WalkDir(basePath, func(path string, d fs.DirEntry, err error) error {
		if workerCtx.Err() != nil {
			return errStopWalk
		}
		if err != nil {
			return nil
		}

		relPath, err := filepath.Rel(basePath, path)
		if err != nil {
			relPath = filepath.Base(path)
		}

		if relPath == "." {
			return nil
		}
		relSlash := filepath.ToSlash(relPath)

		if d.IsDir() {
			if useDefaultExcludes && isDefaultPrunedDir(relSlash) {
				return filepath.SkipDir
			}
			if isExcluded(relSlash, p.Exclude) {
				return filepath.SkipDir
			}
			return nil
		}

		if isExcluded(relSlash, p.Exclude) {
			return nil
		}

		matched, _ := doublestar.PathMatch(filePattern, relSlash)
		if !matched {
			return nil
		}

		info, err := d.Info()
		modTime := int64(0)
		if err == nil {
			modTime = info.ModTime().UnixNano()
		}

		select {
		case taskChan <- grepTask{path: path, relPath: relPath, modTime: modTime}:
		case <-workerCtx.Done():
			return errStopWalk
		}

		return nil
	})

	close(taskChan)
	wg.Wait()

	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, fmt.Errorf("walk error: %w", err)
	}
	if len(matches) == 0 && len(fileCounts) == 0 {
		return &Result{
			Success: true,
			Output:  "No matches found",
		}, nil
	}

	// Sort by modification time (newest first)
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].modTime > matches[j].modTime
	})

	// Limit results
	if len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	// Format output
	var lines []string
	switch mode {
	case "files":
		seen := make(map[string]struct{})
		for _, m := range matches {
			if _, ok := seen[m.file]; ok {
				continue
			}
			seen[m.file] = struct{}{}
			lines = append(lines, m.file)
		}
	case "count":
		paths := make([]string, 0, len(fileCounts))
		for path := range fileCounts {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			lines = append(lines, fmt.Sprintf("%s: %d", path, fileCounts[path]))
		}
	default:
		for _, m := range matches {
			content := m.content
			if len(content) > maxGrepLineLength {
				content = content[:maxGrepLineLength] + "..."
			}
			lines = append(lines, fmt.Sprintf("%s:%d: %s", m.file, m.line, content))
		}
	}

	output := strings.Join(lines, "\n")

	return &Result{
		Success: true,
		Output:  output,
	}, nil
}

func (t *GrepTool) searchFile(fullPath, relPath string, re *regexp.Regexp, modTime int64, maxMatches int, stopAtFirst bool) ([]grepMatch, int) {
	file, err := os.Open(fullPath)
	if err != nil {
		return nil, 0
	}
	defer file.Close()

	// Read first 512 bytes to check for binary
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return nil, 0
	}

	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			// File is binary
			return nil, 0
		}
	}

	var matches []grepMatch
	reader := io.MultiReader(bytes.NewReader(buf[:n]), file)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNum := 0
	totalCount := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if re.MatchString(line) {
			totalCount++
			if maxMatches <= 0 || len(matches) < maxMatches {
				matches = append(matches, grepMatch{
					file:    relPath,
					line:    lineNum,
					content: strings.TrimSpace(line),
					modTime: modTime,
				})
			}
			if stopAtFirst {
				break
			}
		}
	}

	return matches, totalCount
}

// Ensure GrepTool implements Tool
var _ Tool = (*GrepTool)(nil)
