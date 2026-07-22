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
	"github.com/A2gent/brute/internal/storage"
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

func TestGetGeneratedAssetCanonicalizesAllowedSymlinkRoot(t *testing.T) {
	t.Parallel()
	realRoot := t.TempDir()
	linkedParent := t.TempDir()
	linkedRoot := filepath.Join(linkedParent, "data")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	generatedDir := filepath.Join(realRoot, "generated")
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(generatedDir, "artifact.txt")
	if err := os.WriteFile(path, []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := &Server{config: &config.Config{DataPath: linkedRoot}}
	req := httptest.NewRequest(http.MethodGet, "/assets/generated?path="+path, nil)
	rec := httptest.NewRecorder()
	server.handleGetGeneratedAsset(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for canonical generated root, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "generated" {
		t.Fatalf("unexpected generated asset body: %q", rec.Body.String())
	}
}

func TestGetGeneratedAssetMapsDockerWorkspacePathToProjectRoot(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	generatedDir := filepath.Join(projectRoot, "generated", "comfyui")
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(generatedDir, "artifact.txt")
	if err := os.WriteFile(path, []byte("from docker"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveProject(&storage.Project{ID: "project", Name: "Project", Folder: &projectRoot}); err != nil {
		t.Fatal(err)
	}

	server := &Server{config: &config.Config{DataPath: t.TempDir()}, store: store}
	req := httptest.NewRequest(http.MethodGet, "/assets/generated?path=/workspace/generated/comfyui/artifact.txt", nil)
	rec := httptest.NewRecorder()
	server.handleGetGeneratedAsset(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for Docker workspace artifact, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "from docker" {
		t.Fatalf("unexpected Docker workspace artifact body: %q", rec.Body.String())
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
