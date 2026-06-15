package filesearch

import (
	"strings"
)

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
