package filesearch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
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

var defaultPrunedDirs = map[string]struct{}{
	".cache":           {},
	".git":             {},
	".hg":              {},
	".next":            {},
	".nuxt":            {},
	".pnpm-store":      {},
	".svn":             {},
	".turbo":           {},
	"bower_components": {},
	"build":            {},
	"coverage":         {},
	"dist":             {},
	"node_modules":     {},
	"out":              {},
	"target":           {},
	"vendor":           {},
}

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

// Search runs a fast lookup over immutable in-memory data.
func (idx *Index) Search(req SearchRequest) SearchResult {
	if idx == nil {
		return SearchResult{Query: req.Query}
	}
	query := NormalizeQuery(req.Query)
	result := SearchResult{
		Root:  idx.root,
		Query: req.Query,
		Stats: idx.Stats(),
	}
	if query == "" {
		return result
	}
	queryLower := strings.ToLower(query)
	fileLimit := normalizeLimit(req.FileLimit, defaultFileLimit, maxFileLimit)
	contentLimit := normalizeLimit(req.ContentLimit, defaultContentLimit, maxContentLimit)

	result.FileMatches = idx.searchFiles(queryLower, fileLimit)
	if req.IncludeContent {
		result.ContentMatches = idx.searchContent(queryLower, contentLimit)
	}
	return result
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

// NormalizeQuery mirrors Caesar search input behavior: trim quotes, normalize
// slashes and remove leading ./ so pasted file paths match indexed paths.
func NormalizeQuery(query string) string {
	normalized := strings.TrimSpace(query)
	for len(normalized) >= 2 {
		first := normalized[0]
		last := normalized[len(normalized)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') || (first == '`' && last == '`') {
			normalized = strings.TrimSpace(normalized[1 : len(normalized)-1])
			continue
		}
		break
	}
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	for strings.HasPrefix(normalized, "./") {
		normalized = strings.TrimPrefix(normalized, "./")
	}
	normalized = strings.TrimPrefix(normalized, "/")
	return normalized
}

func normalizeLimit(value, fallback, maxValue int) int {
	if value <= 0 {
		value = fallback
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (idx *Index) searchFiles(queryLower string, limit int) []FileMatch {
	queryCompact := compactSearchText(queryLower)
	candidates := make([]FileMatch, 0, limit)
	for _, file := range idx.files {
		score := scorePath(file, queryLower, queryCompact)
		if score <= 0 {
			continue
		}
		candidates = append(candidates, FileMatch{Path: file.path, Name: file.name, Score: score})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Path < candidates[j].Path
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

func scorePath(file fileRecord, queryLower, queryCompact string) int {
	baseLower := strings.ToLower(filepath.Base(filepath.FromSlash(file.path)))
	stemLower := strings.TrimSuffix(baseLower, strings.ToLower(filepath.Ext(baseLower)))
	score := 0
	switch {
	case file.pathLower == queryLower:
		score = 180
	case baseLower == queryLower || stemLower == queryLower:
		score = 160
	case strings.HasPrefix(baseLower, queryLower):
		score = 130
	case strings.Contains(baseLower, queryLower):
		score = 110
	case strings.HasPrefix(file.pathLower, queryLower):
		score = 95
	case strings.Contains(file.pathLower, queryLower):
		score = 75
	case queryCompact != "" && strings.Contains(file.pathCompact, queryCompact):
		score = 68
	default:
		score = fuzzyPathScore(baseLower, stemLower, file.pathLower, queryLower, queryCompact)
	}
	if score <= 0 {
		return 0
	}
	if len(file.path) <= 40 {
		score += 10
	}
	if len(file.path) <= 16 {
		score += 6
	}
	return score
}

func fuzzyPathScore(baseLower, stemLower, pathLower, queryLower, queryCompact string) int {
	if len(queryLower) < 3 {
		return 0
	}
	for _, candidate := range []string{baseLower, stemLower, compactSearchText(baseLower), compactSearchText(stemLower)} {
		if candidate == "" {
			continue
		}
		if ok, distance := withinEditDistance(candidate, queryCompact, fuzzyDistanceLimit(queryCompact)); ok {
			return 58 - distance*8
		}
		if ok, distance := withinEditDistance(candidate, queryLower, fuzzyDistanceLimit(queryLower)); ok {
			return 54 - distance*8
		}
	}
	if isSubsequence(queryCompact, compactSearchText(pathLower)) {
		return 35
	}
	return 0
}

func fuzzyDistanceLimit(query string) int {
	length := len(query)
	switch {
	case length <= 4:
		return 1
	case length <= 10:
		return 2
	default:
		return 3
	}
}

func (idx *Index) searchContent(queryLower string, limit int) []ContentMatch {
	candidateIDs := idx.contentCandidates(queryLower)
	if len(candidateIDs) == 0 {
		return nil
	}
	matches := make([]ContentMatch, 0, limit)
	for _, contentID := range candidateIDs {
		content := idx.contents[contentID]
		matchIndex := strings.Index(content.lowerText, queryLower)
		if matchIndex < 0 {
			continue
		}
		lineNumber, lineText, col := content.lineAt(matchIndex)
		lineScore := 70 - lineNumber
		if lineScore < 12 {
			lineScore = 12
		}
		colScore := 20 - col
		if colScore < 0 {
			colScore = 0
		}
		file := idx.files[content.fileID]
		matches = append(matches, ContentMatch{
			Path:    file.path,
			Line:    lineNumber,
			Preview: truncateText(strings.TrimSpace(lineText), 220),
			Score:   lineScore + colScore + min(len(queryLower), 30),
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].Line < matches[j].Line
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func (idx *Index) contentCandidates(queryLower string) []int {
	grams := uniqueTrigrams(queryLower)
	if len(grams) == 0 {
		ids := make([]int, len(idx.contents))
		for i := range ids {
			ids[i] = i
		}
		return ids
	}
	postings := make([][]int, 0, len(grams))
	for _, gram := range grams {
		posting := idx.contentGrams[gram]
		if len(posting) == 0 {
			return nil
		}
		postings = append(postings, posting)
	}
	sort.Slice(postings, func(i, j int) bool { return len(postings[i]) < len(postings[j]) })
	current := append([]int(nil), postings[0]...)
	for _, posting := range postings[1:] {
		current = intersectSortedInts(current, posting)
		if len(current) == 0 {
			return nil
		}
	}
	return current
}

func (content contentRecord) lineAt(byteIndex int) (lineNumber int, lineText string, column int) {
	lineIdx := sort.Search(len(content.lineStarts), func(i int) bool { return content.lineStarts[i] > byteIndex }) - 1
	if lineIdx < 0 {
		lineIdx = 0
	}
	start := content.lineStarts[lineIdx]
	end := len(content.text)
	if lineIdx+1 < len(content.lineStarts) {
		end = content.lineStarts[lineIdx+1] - 1
	}
	if end < start {
		end = start
	}
	line := strings.TrimRight(content.text[start:end], "\r")
	return lineIdx + 1, line, byteIndex - start
}

func buildLineStarts(text string) []int {
	starts := []int{0}
	for i, r := range text {
		if r == '\n' && i+1 < len(text) {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func shouldSkipDir(relPath string, opts Options) bool {
	base := filepath.Base(filepath.FromSlash(relPath))
	if !opts.IncludeHidden && strings.HasPrefix(base, ".") {
		return true
	}
	if !opts.DisableDefaultExcludes {
		if _, ok := defaultPrunedDirs[base]; ok {
			return true
		}
	}
	return false
}

func shouldSkipFile(relPath string, opts Options) bool {
	if opts.IncludeHidden {
		return false
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(relPath)), "/")
	for _, part := range parts {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}

func looksLikeBinaryOrMedia(relPath string) bool {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".avif", ".bmp", ".gif", ".heic", ".heif", ".ico", ".jpeg", ".jpg", ".png", ".svg", ".svgz", ".tif", ".tiff", ".webp":
		return true
	case ".3g2", ".3gp", ".avi", ".flv", ".m4v", ".mkv", ".mov", ".mp4", ".mpeg", ".mpg", ".ogv", ".webm", ".wmv":
		return true
	default:
		return false
	}
}

func isIndexableText(content []byte, maxLines int) bool {
	if bytes.Contains(content, []byte{0}) || !utf8.Valid(content) {
		return false
	}
	if countLines(content) > maxLines {
		return false
	}
	return true
}

func countLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lines := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		lines++
	}
	return lines
}

func addTextTrigrams(index map[uint32][]int, text string, id int) {
	grams := uniqueTrigrams(text)
	for _, gram := range grams {
		index[gram] = append(index[gram], id)
	}
}

func uniqueTrigrams(text string) []uint32 {
	if len(text) < 3 {
		return nil
	}
	seen := make(map[uint32]struct{})
	for i := 0; i+2 < len(text); i++ {
		gram := uint32(text[i])<<16 | uint32(text[i+1])<<8 | uint32(text[i+2])
		seen[gram] = struct{}{}
	}
	grams := make([]uint32, 0, len(seen))
	for gram := range seen {
		grams = append(grams, gram)
	}
	sort.Slice(grams, func(i, j int) bool { return grams[i] < grams[j] })
	return grams
}

func intersectSortedInts(left, right []int) []int {
	out := left[:0]
	i, j := 0, 0
	for i < len(left) && j < len(right) {
		switch {
		case left[i] == right[j]:
			out = append(out, left[i])
			i++
			j++
		case left[i] < right[j]:
			i++
		default:
			j++
		}
	}
	return out
}

func compactSearchText(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func withinEditDistance(candidate, query string, limit int) (bool, int) {
	if candidate == "" || query == "" || limit < 0 {
		return false, 0
	}
	if strings.Contains(candidate, query) {
		return true, 0
	}
	best := limit + 1
	for _, segment := range candidateSegments(candidate, len(query), limit) {
		distance := boundedLevenshtein(segment, query, limit)
		if distance < best {
			best = distance
		}
		if best <= limit {
			return true, best
		}
	}
	return false, best
}

func candidateSegments(candidate string, queryLen, limit int) []string {
	if len(candidate) <= queryLen+limit {
		return []string{candidate}
	}
	segments := make([]string, 0, limit*2+1)
	minLen := queryLen - limit
	if minLen < 1 {
		minLen = 1
	}
	maxLen := queryLen + limit
	for start := 0; start < len(candidate); start++ {
		for length := minLen; length <= maxLen; length++ {
			end := start + length
			if end > len(candidate) {
				continue
			}
			segments = append(segments, candidate[start:end])
		}
	}
	return segments
}

func boundedLevenshtein(a, b string, limit int) int {
	if abs(len(a)-len(b)) > limit {
		return limit + 1
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		rowMin := curr[0]
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
			if curr[j] < rowMin {

				rowMin = curr[j]
			}
		}
		if rowMin > limit {
			return limit + 1
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func isSubsequence(needle, haystack string) bool {
	if needle == "" {
		return false
	}
	pos := 0
	for i := 0; i < len(haystack) && pos < len(needle); i++ {
		if haystack[i] == needle[pos] {
			pos++
		}
	}
	return pos == len(needle)
}

func estimatedFileRecordBytes(file fileRecord) int64 {
	return int64(len(file.path)+len(file.pathLower)+len(file.pathCompact)+len(file.name)) + 96
}

func estimatedContentIndexBytes(size int64) int64 {
	// Content is stored twice (original + lower-case) plus line offsets and
	// trigram postings. This conservative estimate keeps the process cache under
	// the configured memory budget without expensive exact accounting per file.
	return size*12 + 4096
}

func approximateIndexBytes(idx *Index) int64 {
	var total int64
	for _, file := range idx.files {
		total += estimatedFileRecordBytes(file)
	}
	for _, content := range idx.contents {
		total += int64(len(content.text)+len(content.lowerText)) + int64(len(content.lineStarts))*8 + 96
	}
	for _, posting := range idx.contentGrams {
		total += int64(len(posting))*8 + 16
	}
	return total
}

func truncateText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
