package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadDevReloadStatus(t *testing.T) {
	prev := devReloadStatusPath
	t.Cleanup(func() { devReloadStatusPath = prev })

	devReloadStatusPath = ""
	if got := readDevReloadStatus(); got != "" {
		t.Fatalf("outside just dev status = %q, want empty", got)
	}

	devReloadStatusPath = filepath.Join(t.TempDir(), "status")
	if got := readDevReloadStatus(); got != "DEV" {
		t.Fatalf("missing file status = %q, want DEV marker", got)
	}
	if err := os.WriteFile(devReloadStatusPath, []byte("DEV: rebuilding...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readDevReloadStatus(); got != "DEV: rebuilding..." {
		t.Fatalf("status = %q", got)
	}
}
