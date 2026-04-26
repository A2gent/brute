package tools

import (
	"context"
	"path/filepath"
	"os"
	"io/fs"
	"fmt"
	"github.com/bmatcuk/doublestar/v4"
)

// To be pasted into find_files.go
func collectFileMatchesNew(ctx context.Context, basePath, pattern string, exclude []string, showHidden bool, limit int) ([]fileSearchResult, int, error) {
	if limit <= 0 {
		limit = 1000 // default or unbounded, let's say max allowed or we'll grow dynamically
	}
	var results []fileSearchResult
	totalIncluded := 0

	err := filepath.WalkDir(basePath, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil // ignore permission errors and such
		}

		rel, err := filepath.Rel(basePath, path)
		if err != nil {
			rel = filepath.Base(path)
		}

		if rel == "." {
			return nil
		}

		// Convert rel to slash-separated for glob matching
		relSlash := filepath.ToSlash(rel)

		// Directory pruning
		if d.IsDir() {
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
			// Also check against base pattern just in case like old doublestar did?
			// Actually the old code did doublestar.FilepathGlob(filepath.Join(basePath, pattern)).
			// So `pattern` here might be `**/*.go` or `*.go`.
			return nil
		}

		totalIncluded++
		if len(results) < limit {
			info, err := d.Info()
			modTime := int64(0)
			if err == nil {
				modTime = info.ModTime().UnixNano()
			}
			results = append(results, fileSearchResult{path: relSlash, modTime: modTime})
		}

		return nil
	})

	return results, totalIncluded, err
}
