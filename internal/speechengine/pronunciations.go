package speechengine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Pronunciation overrides let users fix word readings without rebuilding brute.
// Two JSON files of {"word": "reading"} live in <data>/speech/:
//   - pronunciations.json        hand-edited exceptions, highest priority
//   - pronunciations-cache.json  machine-written readings (filled by putCachedPronunciation)
//
// Lookup order: user file > cache > built-in dictionaries. "+" marks the stressed vowel.
const (
	userPronunciationsFile  = "pronunciations.json"
	cachedPronunciationFile = "pronunciations-cache.json"
)

type pronunciationFile struct {
	mu      sync.Mutex
	name    string
	path    string
	modTime time.Time
	words   map[string]string
}

var (
	userPronunciations   = &pronunciationFile{name: userPronunciationsFile}
	cachedPronunciations = &pronunciationFile{name: cachedPronunciationFile}
)

// lookupPronunciation returns a user or cached reading for word.
func lookupPronunciation(word string) (string, bool) {
	key := strings.ToLower(word)
	if r, ok := userPronunciations.get(key); ok {
		return r, true
	}
	return cachedPronunciations.get(key)
}

// putCachedPronunciation stores a machine-generated reading; user entries still win.
func putCachedPronunciation(word, reading string) error {
	return cachedPronunciations.put(strings.ToLower(word), reading)
}

func (f *pronunciationFile) resolvePath() string {
	return filepath.Join(resolveSpeechDataPath(), "speech", f.name)
}

// reload re-reads the file only when its mtime or location changed, so edits apply
// without a restart while the hot path stays a single stat call.
func (f *pronunciationFile) reload() {
	path := f.resolvePath()
	info, err := os.Stat(path)
	if err != nil {
		f.path, f.modTime, f.words = path, time.Time{}, nil
		return
	}
	if path == f.path && info.ModTime().Equal(f.modTime) {
		return
	}
	f.path, f.modTime, f.words = path, info.ModTime(), nil
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	raw := map[string]string{}
	if json.Unmarshal(data, &raw) != nil {
		return // malformed file: ignore rather than break synthesis
	}
	f.words = make(map[string]string, len(raw))
	for k, v := range raw {
		if v = strings.TrimSpace(v); v != "" {
			f.words[strings.ToLower(strings.TrimSpace(k))] = v
		}
	}
}

func (f *pronunciationFile) get(key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reload()
	r, ok := f.words[key]
	return r, ok
}

func (f *pronunciationFile) put(key, reading string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reload()
	next := make(map[string]string, len(f.words)+1)
	for k, v := range f.words {
		next[k] = v
	}
	next[key] = reading
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	path := f.resolvePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Write-then-rename so a crash never leaves a half-written file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	f.path = ""
	return nil
}
