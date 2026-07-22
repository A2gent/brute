package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestGetGeneratedAssetServesSupportedMedia(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	generatedDir := filepath.Join(root, "generated", "comfyui")
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(generatedDir, "sound.flac")
	if err := os.WriteFile(path, []byte("fLaC-test-audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := &Server{config: &config.Config{DataPath: root}}
	req := httptest.NewRequest(http.MethodGet, "/assets/generated?path="+path, nil)
	rec := httptest.NewRecorder()
	server.handleGetGeneratedAsset(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "audio/") {
		t.Fatalf("expected audio content type, got %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "sound.flac") {
		t.Fatalf("expected filename disposition, got %q", got)
	}
}

func TestGetGeneratedAssetRejectsPathOutsideGeneratedRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := &Server{config: &config.Config{DataPath: root}}
	req := httptest.NewRequest(http.MethodGet, "/assets/generated?path="+outside, nil)
	rec := httptest.NewRecorder()
	server.handleGetGeneratedAsset(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetGeneratedAssetRejectsOversizedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	generatedDir := filepath.Join(root, "generated")
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(generatedDir, "huge.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxGeneratedAssetBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()

	server := &Server{config: &config.Config{DataPath: root}}
	req := httptest.NewRequest(http.MethodGet, "/assets/generated?path="+path, nil).WithContext(context.Background())
	rec := httptest.NewRecorder()
	server.handleGetGeneratedAsset(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}
