package speechengine

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// PronunciationResolver asks an LLM for Russian readings of unknown Latin words.
// It is set by the HTTP server; when nil, unknown words fall back to rough transliteration.
// Returned readings use American pronunciation with "+" before the stressed vowel.
var PronunciationResolver func(ctx context.Context, words []string) (map[string]string, error)

const (
	maxWordsPerLookup = 30
	lookupTimeout     = 8 * time.Second
)

var (
	// triedWords remembers words the LLM was already asked about (even on failure),
	// so a bad answer or outage does not add latency to every synthesis.
	triedWords   = map[string]struct{}{}
	triedWordsMu sync.Mutex
	reReading    = regexp.MustCompile(`^[а-яё+\- ]+$`)
)

// isKnownLatin reports whether latinToCyrillic can read the word without guessing.
func isKnownLatin(lower string) bool {
	if _, ok := lookupPronunciation(lower); ok {
		return true
	}
	if _, ok := latinDict[lower]; ok {
		return true
	}
	if _, ok := latinInflected(lower); ok {
		return true
	}
	return len(lower) < 2 || strings.ContainsAny(lower, "0123456789")
}

// unknownLatinWords returns distinct lowercase Latin words that would be transliterated by guesswork.
func unknownLatinWords(text string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, w := range reLatin.FindAllString(text, -1) {
		lower := strings.ToLower(strings.ReplaceAll(w, "’", "'"))
		if _, dup := seen[lower]; dup || isKnownLatin(lower) || strings.Contains(lower, "'") {
			continue
		}
		// Short ALL-CAPS tokens are spelled out as acronyms, no LLM needed.
		if n := len(w); n <= 5 && w == strings.ToUpper(w) {
			continue
		}
		seen[lower] = struct{}{}
		out = append(out, lower)
	}
	return out
}

// prefetchPronunciations resolves unknown words via the LLM and stores them in the cache
// so the following normalizeRussianText call reads them correctly. Errors are ignored on purpose.
func prefetchPronunciations(ctx context.Context, text string) {
	if PronunciationResolver == nil {
		return
	}
	triedWordsMu.Lock()
	var words []string
	for _, w := range unknownLatinWords(text) {
		if _, tried := triedWords[w]; !tried && len(words) < maxWordsPerLookup {
			words = append(words, w)
			triedWords[w] = struct{}{}
		}
	}
	triedWordsMu.Unlock()
	if len(words) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	readings, err := PronunciationResolver(ctx, words)
	if err != nil {
		return
	}
	for _, w := range words {
		r := strings.ToLower(strings.TrimSpace(readings[w]))
		if reReading.MatchString(r) {
			_ = putCachedPronunciation(w, r)
		}
	}
}
