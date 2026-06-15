package filesearch

import (
	"path/filepath"
	"sort"
	"strings"
)

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
