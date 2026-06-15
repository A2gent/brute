package filesearch

import (
	"bytes"
	"sort"
	"strings"
	"unicode/utf8"
)

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
