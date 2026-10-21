package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLog(t *testing.T, dir, name string, size int, age time.Duration) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestPruneJSONLLogsBySizeDropsOldestAndKeepsActive(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "old.jsonl", 100, 48*time.Hour)
	writeLog(t, dir, "mid.jsonl", 100, 24*time.Hour)
	writeLog(t, dir, "active.jsonl", 100, time.Minute)
	writeLog(t, dir, "note.txt", 1000, 72*time.Hour)

	n, freed := PruneJSONLLogs(dir, RetentionPolicy{MaxBytes: 100}, time.Now())
	if n != 2 || freed != 200 {
		t.Fatalf("deleted=%d freed=%d, want 2/200", n, freed)
	}
	if exists(dir, "old.jsonl") || exists(dir, "mid.jsonl") || !exists(dir, "active.jsonl") || !exists(dir, "note.txt") {
		t.Fatal("unexpected survivors")
	}
}

func TestPruneJSONLLogsByAgeAndCount(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "a.jsonl", 1, 72*time.Hour)
	writeLog(t, dir, "b.jsonl", 1, 3*time.Hour)
	writeLog(t, dir, "c.jsonl", 1, 2*time.Hour)

	PruneJSONLLogs(dir, RetentionPolicy{MaxAge: 24 * time.Hour}, time.Now())
	if exists(dir, "a.jsonl") || !exists(dir, "b.jsonl") {
		t.Fatal("age limit not applied")
	}
	PruneJSONLLogs(dir, RetentionPolicy{MaxFiles: 1}, time.Now())
	if exists(dir, "b.jsonl") || !exists(dir, "c.jsonl") {
		t.Fatal("count limit not applied")
	}
}
