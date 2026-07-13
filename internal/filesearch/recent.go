package filesearch

import (
	"container/heap"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RecentFile is a project file ranked by modification time.
type RecentFile struct {
	Path       string
	Name       string
	ModifiedAt time.Time
}

type recentFileHeap []RecentFile

func (h recentFileHeap) Len() int { return len(h) }

func (h recentFileHeap) Less(i, j int) bool {
	if h[i].ModifiedAt.Equal(h[j].ModifiedAt) {
		return h[i].Path < h[j].Path
	}
	return h[i].ModifiedAt.Before(h[j].ModifiedAt)
}

func (h recentFileHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *recentFileHeap) Push(x any) {
	*h = append(*h, x.(RecentFile))
}

func (h *recentFileHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// RecentFiles walks the project tree and returns the most recently modified files.
// It skips dependency folders and hidden paths using the same rules as indexing.
func RecentFiles(ctx context.Context, root string, limit int, opts Options) ([]RecentFile, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 5
	}
	resolvedRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return nil, fmt.Errorf("resolve recent files root: %w", err)
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("recent files root is not a directory: %s", resolvedRoot)
	}

	opts = normalizeOptions(opts)
	top := make(recentFileHeap, 0, limit)
	heap.Init(&top)

	err = filepath.WalkDir(resolvedRoot, func(fullPath string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return nil
		}
		if fullPath == resolvedRoot {
			return nil
		}

		relPath, relErr := filepath.Rel(resolvedRoot, fullPath)
		if relErr != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		if entry.IsDir() {
			if shouldSkipDir(relPath, opts) {
				return filepath.SkipDir
			}
			return nil
		}

		if shouldSkipFile(relPath, opts) || looksLikeBinaryOrMedia(relPath) {
			return nil
		}

		fileInfo, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}

		candidate := RecentFile{
			Path:       relPath,
			Name:       entry.Name(),
			ModifiedAt: fileInfo.ModTime(),
		}
		if top.Len() < limit {
			heap.Push(&top, candidate)
			return nil
		}
		if candidate.ModifiedAt.After(top[0].ModifiedAt) {
			top[0] = candidate
			heap.Fix(&top, 0)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	results := make([]RecentFile, top.Len())
	for i := len(results) - 1; i >= 0; i-- {
		results[i] = heap.Pop(&top).(RecentFile)
	}
	return results, nil
}
