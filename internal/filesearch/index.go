package filesearch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultMaxMemoryBytes  int64 = 1 << 30   // 1 GiB process-level cache budget.
	DefaultMaxContentBytes int64 = 256 << 20 // Keep room for lower-case text and posting lists.
	DefaultMaxFileBytes    int64 = 512 << 10 // Matches Caesar editor text-file limit.
	DefaultMaxFileLines          = 20000
	DefaultStaleAfter            = 30 * time.Second
)

const (
	defaultFileLimit    = 20
	defaultContentLimit = 5
	maxFileLimit        = 500
	maxContentLimit     = 500
)

var errStopWalk = errors.New("stop walk")

// Options controls how a project index is built. Zero values intentionally pick
// conservative defaults so callers can use Build(ctx, root, Options{}).
type Options struct {
	IncludeHidden          bool
	DisableDefaultExcludes bool
	MaxContentBytes        int64
	MaxFileBytes           int64
	MaxFileLines           int
	MaxIndexBytes          int64
}

// SearchRequest describes one in-memory lookup against an existing index.
type SearchRequest struct {
	Query          string
	FileLimit      int
	ContentLimit   int
	IncludeContent bool
}

// FileMatch is a ranked project path match.
type FileMatch struct {
	Path  string
	Name  string
	Score int
}

// ContentMatch is a ranked first-line content match.
type ContentMatch struct {
	Path    string
	Line    int
	Preview string
	Score   int
}

// Stats exposes lightweight index/search diagnostics for API/tool metadata.
type Stats struct {
	Root                string `json:"root"`
	IndexedFiles        int    `json:"indexed_files"`
	IndexedContentFiles int    `json:"indexed_content_files"`
	SkippedFiles        int    `json:"skipped_files"`
	TotalContentBytes   int64  `json:"total_content_bytes"`
	ApproxBytes         int64  `json:"approx_bytes"`
	Truncated           bool   `json:"truncated"`
	BuildDurationMS     int64  `json:"build_duration_ms"`
	IndexAgeMS          int64  `json:"index_age_ms"`
}

// SearchResult contains both path and optional content matches.
type SearchResult struct {
	Root           string
	Query          string
	FileMatches    []FileMatch
	ContentMatches []ContentMatch
	Stats          Stats
}

type fileRecord struct {
	path        string
	name        string
	pathLower   string
	pathCompact string
	modTime     int64
	contentID   int
}

type contentRecord struct {
	fileID     int
	text       string
	lowerText  string
	lineStarts []int
}

// Index is an immutable, per-project in-memory file-search index.
type Index struct {
	root         string
	builtAt      time.Time
	files        []fileRecord
	contents     []contentRecord
	contentGrams map[uint32][]int
	stats        Stats
}

// Build walks root once and creates a lightweight immutable index. It avoids
// dependency folders and bounds indexed content bytes so large projects do not
// turn UI search into a CPU/RAM-heavy operation.
func Build(ctx context.Context, root string, opts Options) (*Index, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resolvedRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return nil, fmt.Errorf("resolve index root: %w", err)
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("index root is not a directory: %s", resolvedRoot)
	}

	opts = normalizeOptions(opts)
	idx := &Index{
		root:         resolvedRoot,
		builtAt:      time.Now(),
		contentGrams: make(map[uint32][]int),
	}
	start := time.Now()
	var indexedContentBytes int64
	var estimatedIndexBytes int64

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

		if shouldSkipFile(relPath, opts) {
			idx.stats.SkippedFiles++
			return nil
		}

		fileInfo, infoErr := entry.Info()
		modTime := int64(0)
		size := int64(-1)
		if infoErr == nil {
			modTime = fileInfo.ModTime().UnixNano()
			size = fileInfo.Size()
		}

		fileID := len(idx.files)
		file := fileRecord{
			path:        relPath,
			name:        entry.Name(),
			pathLower:   strings.ToLower(relPath),
			pathCompact: compactSearchText(relPath),
			modTime:     modTime,
			contentID:   -1,
		}
		idx.files = append(idx.files, file)
		estimatedIndexBytes += estimatedFileRecordBytes(file)

		if size < 0 || size > opts.MaxFileBytes || indexedContentBytes+size > opts.MaxContentBytes || estimatedIndexBytes+estimatedContentIndexBytes(size) > opts.MaxIndexBytes || looksLikeBinaryOrMedia(relPath) {
			if size > 0 && (indexedContentBytes+size > opts.MaxContentBytes || estimatedIndexBytes+estimatedContentIndexBytes(size) > opts.MaxIndexBytes) {
				idx.stats.Truncated = true
			}
			return nil
		}

		content, readErr := os.ReadFile(fullPath)
		if readErr != nil || !isIndexableText(content, opts.MaxFileLines) {
			idx.stats.SkippedFiles++
			return nil
		}
		contentEstimate := estimatedContentIndexBytes(int64(len(content)))
		if int64(len(content))+indexedContentBytes > opts.MaxContentBytes || estimatedIndexBytes+contentEstimate > opts.MaxIndexBytes {
			idx.stats.Truncated = true
			return nil
		}

		contentID := len(idx.contents)
		text := string(content)
		lowerText := strings.ToLower(text)
		idx.files[fileID].contentID = contentID
		idx.contents = append(idx.contents, contentRecord{
			fileID:     fileID,
			text:       text,
			lowerText:  lowerText,
			lineStarts: buildLineStarts(text),
		})
		indexedContentBytes += int64(len(content))
		estimatedIndexBytes += contentEstimate
		addTextTrigrams(idx.contentGrams, lowerText, contentID)
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, err
	}

	idx.stats.Root = resolvedRoot
	idx.stats.IndexedFiles = len(idx.files)
	idx.stats.IndexedContentFiles = len(idx.contents)
	idx.stats.TotalContentBytes = indexedContentBytes
	idx.stats.BuildDurationMS = time.Since(start).Milliseconds()
	idx.stats.ApproxBytes = approximateIndexBytes(idx)
	return idx, nil
}

func normalizeOptions(opts Options) Options {
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultMaxFileBytes
	}
	if opts.MaxContentBytes <= 0 {
		opts.MaxContentBytes = DefaultMaxContentBytes
	}
	if opts.MaxFileLines <= 0 {
		opts.MaxFileLines = DefaultMaxFileLines
	}
	if opts.MaxIndexBytes <= 0 {
		opts.MaxIndexBytes = DefaultMaxMemoryBytes
	}
	return opts
}

// Stats returns a copy of current index stats with age filled in.
func (idx *Index) Stats() Stats {
	if idx == nil {
		return Stats{}
	}
	stats := idx.stats
	stats.IndexAgeMS = time.Since(idx.builtAt).Milliseconds()
	return stats
}
