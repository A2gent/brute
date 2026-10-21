package session

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RetentionPolicy limits how much JSONL session history is kept on disk.
// A zero field disables that limit.
type RetentionPolicy struct {
	MaxBytes int64
	MaxAge   time.Duration
	MaxFiles int
}

// retentionActiveWindow protects logs that are probably still being written.
const retentionActiveWindow = 10 * time.Minute

// JSONLFolder returns the folder currently used for JSONL logs ("" when disabled).
func (m *Manager) JSONLFolder() string {
	if m == nil {
		return ""
	}
	return m.jsonlWriter.Folder()
}

// PruneJSONLLogs deletes the oldest <id>.jsonl files in folder until every limit of
// the policy is satisfied. Recently modified files are never removed. Returns the
// number of deleted files and the bytes freed.
func PruneJSONLLogs(folder string, p RetentionPolicy, now time.Time) (int, int64) {
	if folder == "" || (p.MaxBytes <= 0 && p.MaxAge <= 0 && p.MaxFiles <= 0) {
		return 0, 0
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		return 0, 0
	}
	type logFile struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []logFile
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, logFile{filepath.Join(folder, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	// Oldest first: they are the first candidates for removal.
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })

	deleted, freed := 0, int64(0)
	remaining := len(files)
	for _, f := range files {
		if now.Sub(f.mtime) < retentionActiveWindow {
			break // sorted, so everything after is newer still
		}
		tooOld := p.MaxAge > 0 && now.Sub(f.mtime) > p.MaxAge
		tooMany := p.MaxFiles > 0 && remaining > p.MaxFiles
		tooBig := p.MaxBytes > 0 && total > p.MaxBytes
		if !tooOld && !tooMany && !tooBig {
			break
		}
		if err := os.Remove(f.path); err != nil {
			continue
		}
		deleted++
		freed += f.size
		total -= f.size
		remaining--
	}
	return deleted, freed
}
