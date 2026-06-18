package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleProjectGitHistory_NoGitMetadataReturnsEmptyHistory(t *testing.T) {
	server, projectID, projectDir := newProjectFileTestServer(t)

	target := "/projects/git/history?projectID=" + url.QueryEscape(projectID) + "&limit=160"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected OK for non-git project root, got %d: %s", rec.Code, rec.Body.String())
	}

	var response ProjectGitHistoryResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.RootFolder != projectDir {
		t.Fatalf("expected root folder %q, got %q", projectDir, response.RootFolder)
	}
	if len(response.Branches) != 0 || len(response.Commits) != 0 || response.CurrentBranch != "" {
		t.Fatalf("expected empty history for non-git root, got %#v", response)
	}
}

func TestHandleProjectGitStatusAcceptsAbsoluteRepoPathInsideProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	server, projectID, projectDir := newProjectFileTestServer(t)
	repoDir := filepath.Join(projectDir, "packages", "app")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("failed to create nested repo: %v", err)
	}
	initMindGitTestRepo(t, repoDir)

	target := "/projects/git/status?projectID=" + url.QueryEscape(projectID) + "&repoPath=" + url.QueryEscape(repoDir)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected OK for absolute repo path inside project, got %d: %s", rec.Code, rec.Body.String())
	}

	var response ProjectGitStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.RootFolder != repoDir {
		t.Fatalf("expected root folder %q, got %q", repoDir, response.RootFolder)
	}
	if !response.HasGit {
		t.Fatalf("expected nested repo to be detected as git repo: %#v", response)
	}
}

func TestHandleProjectGitStatusRejectsOutsideAbsoluteRepoPathWithProjectRootMessage(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	outsideDir := t.TempDir()

	target := "/projects/git/status?projectID=" + url.QueryEscape(projectID) + "&repoPath=" + url.QueryEscape(outsideDir)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for outside absolute repo path, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "project root") {
		t.Fatalf("expected project root validation message, got %s", body)
	}
	if strings.Contains(body, "My Mind") {
		t.Fatalf("project git validation should not mention My Mind, got %s", body)
	}
}
