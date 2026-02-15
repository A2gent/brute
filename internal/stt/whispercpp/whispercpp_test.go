package whispercpp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeLanguage(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: ""},
		{input: "  ", want: ""},
		{input: "en", want: "en"},
		{input: "ru-RU", want: "ru"},
		{input: "PT-BR", want: "pt"},
		{input: "auto", want: "auto"},
	}

	for _, tc := range tests {
		got := normalizeLanguage(tc.input)
		if got != tc.want {
			t.Fatalf("normalizeLanguage(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestResolveWhisperSourceDirCopiesModuleCacheToWritableDataDir(t *testing.T) {
	tmpDir := t.TempDir()
	moduleRoot := filepath.Join(tmpDir, "gomodcache")
	moduleSource := filepath.Join(moduleRoot, "github.com", "ggerganov", "whisper.cpp@v1.8.3")
	if err := os.MkdirAll(moduleSource, 0o755); err != nil {
		t.Fatalf("mkdir module source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(moduleSource, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.5)\n"), 0o644); err != nil {
		t.Fatalf("write CMakeLists: %v", err)
	}
	if err := os.WriteFile(filepath.Join(moduleSource, "dummy.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write dummy file: %v", err)
	}
	// Simulate read-only module cache.
	if err := os.Chmod(moduleSource, 0o555); err != nil {
		t.Fatalf("chmod module source: %v", err)
	}
	defer os.Chmod(moduleSource, 0o755)

	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}

	t.Setenv("GOMODCACHE", moduleRoot)
	t.Setenv("AAGENT_DATA_PATH", dataDir)
	t.Setenv("AAGENT_WHISPER_SOURCE", "")

	got, err := resolveWhisperSourceDir()
	if err != nil {
		t.Fatalf("resolveWhisperSourceDir failed: %v", err)
	}
	if got == moduleSource {
		t.Fatalf("expected writable copied source, got module cache path: %s", got)
	}
	wantPrefix := filepath.Join(dataDir, "speech", "whisper", "source")
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("expected source under %s, got %s", wantPrefix, got)
	}
	if _, err := os.Stat(filepath.Join(got, "CMakeLists.txt")); err != nil {
		t.Fatalf("copied source missing CMakeLists.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(got, ".writable-check"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("copied source is not writable: %v", err)
	}
}

func TestResetBuildDirIfSourceMismatch(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatalf("mkdir build dir: %v", err)
	}

	cache := "CMAKE_HOME_DIRECTORY:INTERNAL=/tmp/old-source\nOTHER:STRING=value\n"
	if err := os.WriteFile(filepath.Join(buildDir, "CMakeCache.txt"), []byte(cache), 0o644); err != nil {
		t.Fatalf("write CMakeCache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "stale.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	if err := resetBuildDirIfSourceMismatch(buildDir, "/tmp/new-source"); err != nil {
		t.Fatalf("resetBuildDirIfSourceMismatch failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(buildDir, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected build dir to be reset; stale marker still exists err=%v", err)
	}
	if _, err := os.Stat(buildDir); err != nil {
		t.Fatalf("expected build dir recreated: %v", err)
	}
}
