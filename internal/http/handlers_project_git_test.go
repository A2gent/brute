package http

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleProjectGitUnstageFileUnstagesRemovedDirectoryRecursively(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)

	folderPath := filepath.Join(repoDir, "large-folder")
	if err := os.MkdirAll(folderPath, 0o755); err != nil {
		t.Fatalf("failed to create staged folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folderPath, "one.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("failed to write first staged file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folderPath, "two.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("failed to write second staged file: %v", err)
	}
	runGitForMindTest(t, repoDir, "add", "--", "large-folder")
	if err := os.RemoveAll(folderPath); err != nil {
		t.Fatalf("failed to move staged folder out of repository: %v", err)
	}

	target := "/projects/git/unstage?projectID=" + url.QueryEscape(projectID)
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"path":"large-folder"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected directory unstage to succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	status, err := runGitCommandPreserveLeading(repoDir, "status", "--porcelain=v1")
	if err != nil {
		t.Fatalf("failed to read git status: %v", err)
	}
	if strings.TrimSpace(status) != "" {
		t.Fatalf("expected removed staged directory to disappear from git status, got %q", status)
	}
}
