package filesearch

import (
	"sort"
	"strings"
)

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
