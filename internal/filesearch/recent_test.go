package filesearch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecentFilesReturnsMostRecentlyModifiedFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "old.txt", "old\n")
	writeTestFile(t, root, "middle.txt", "middle\n")
	writeTestFile(t, root, "newest.txt", "newest\n")
	writeTestFile(t, root, "node_modules/pkg/ignored.txt", "ignored\n")

	oldPath := filepath.Join(root, "old.txt")
	middlePath := filepath.Join(root, "middle.txt")
	newestPath := filepath.Join(root, "newest.txt")
	mustTouch(t, oldPath, time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	mustTouch(t, middlePath, time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC))
	mustTouch(t, newestPath, time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC))

	files, err := RecentFiles(context.Background(), root, 2, Options{})
	if err != nil {
		t.Fatalf("RecentFiles returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Path != "newest.txt" || files[1].Path != "middle.txt" {
		t.Fatalf("unexpected order: %+v", files)
	}
}

func mustTouch(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("Chtimes(%q): %v", path, err)
	}
}
