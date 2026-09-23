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

func TestHandleProjectGitBranchChangesReportsMovedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)

	oldRel := filepath.Join("app", "assets", "javascripts", "store", "all.js")
	newRel := filepath.Join("app", "javascript", "src", "store", "index.js")
	if err := os.MkdirAll(filepath.Join(repoDir, filepath.Dir(oldRel)), 0o755); err != nil {
		t.Fatalf("failed to create old directory: %v", err)
	}
	writeGitTestFile(t, repoDir, oldRel, "export default {}\n")
	runGitForMindTest(t, repoDir, "add", "--", oldRel)
	runGitForMindTest(t, repoDir, "-c", "commit.gpgsign=false", "commit", "-m", "add store")
	runGitForMindTest(t, repoDir, "checkout", "-b", "move-store")
	if err := os.MkdirAll(filepath.Join(repoDir, filepath.Dir(newRel)), 0o755); err != nil {
		t.Fatalf("failed to create new directory: %v", err)
	}
	runGitForMindTest(t, repoDir, "mv", oldRel, newRel)
	runGitForMindTest(t, repoDir, "-c", "commit.gpgsign=false", "commit", "-m", "move store")

	target := "/projects/git/branch-changes?projectID=" + url.QueryEscape(projectID) + "&baseBranch=master"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected OK for branch changes, got %d: %s", rec.Code, rec.Body.String())
	}

	var response ProjectGitBranchChangesResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode branch changes: %v", err)
	}

	var moved *ProjectGitCommitFile
	for i := range response.Files {
		if strings.HasPrefix(strings.ToUpper(response.Files[i].Status), "R") {
			moved = &response.Files[i]
			break
		}
	}
	if moved == nil {
		t.Fatalf("expected a renamed file in %#v", response.Files)
	}
	if moved.Path != "app/javascript/src/store/index.js" {
		t.Fatalf("moved path = %q", moved.Path)
	}
	if moved.OldPath != "app/assets/javascripts/store/all.js" {
		t.Fatalf("moved old_path = %q", moved.OldPath)
	}
}

func TestHandleProjectGitBranchDiffIncludesFileSidesForHunkContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)

	oldContent := strings.Join([]string{
		"line one",
		"line two",
		"line three",
		"line four",
		"line five",
		"line six",
		"line seven",
		"line eight",
		"",
	}, "\n")
	newContent := strings.Join([]string{
		"line one",
		"line two",
		"line three",
		"line four changed",
		"line five",
		"line six",
		"line seven",
		"line eight",
		"",
	}, "\n")
	if err := os.MkdirAll(filepath.Join(repoDir, "src"), 0o755); err != nil {
		t.Fatalf("failed to create src directory: %v", err)
	}
	writeGitTestFile(t, repoDir, "src/app.ts", oldContent)
	runGitForMindTest(t, repoDir, "add", "--", "src/app.ts")
	runGitForMindTest(t, repoDir, "-c", "commit.gpgsign=false", "commit", "-m", "add app")
	runGitForMindTest(t, repoDir, "checkout", "-b", "context-expand")
	writeGitTestFile(t, repoDir, "src/app.ts", newContent)
	runGitForMindTest(t, repoDir, "add", "--", "src/app.ts")
	runGitForMindTest(t, repoDir, "-c", "commit.gpgsign=false", "commit", "-m", "change middle")

	target := "/projects/git/branch-diff?projectID=" + url.QueryEscape(projectID) + "&path=" + url.QueryEscape("src/app.ts") + "&baseBranch=master"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected OK for branch diff, got %d: %s", rec.Code, rec.Body.String())
	}

	var response ProjectGitBranchDiffResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode branch diff: %v", err)
	}
	if !strings.Contains(response.Preview, "@@") {
		t.Fatalf("expected a hunk header in preview, got %q", response.Preview)
	}
	if response.OldContent == nil || *response.OldContent != oldContent {
		t.Fatalf("old_content = %#v, want exact master file", response.OldContent)
	}
	if response.NewContent == nil || *response.NewContent != newContent {
		t.Fatalf("new_content = %#v, want exact HEAD file", response.NewContent)
	}
}
