package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestSessionWorktreeBaseRefAndExistingBranches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)
	remoteDir := t.TempDir()
	runGitForMindTest(t, remoteDir, "init", "--bare")
	runGitForMindTest(t, repoDir, "remote", "add", "origin", remoteDir)
	commitOnBranch := func(branch string, from string, file string) {
		runGitForMindTest(t, repoDir, "switch", "-c", branch, from)
		writeGitTestFile(t, repoDir, file, branch+"\n")
		runGitForMindTest(t, repoDir, "add", file)
		runGitForMindTest(t, repoDir, "-c", "commit.gpgsign=false", "commit", "-m", branch)
		runGitForMindTest(t, repoDir, "switch", "master")
	}
	commitOnBranch("feature/base", "master", "base.txt")
	commitOnBranch("feature/local", "master", "local.txt")
	commitOnBranch("feature/remote", "master", "remote.txt")
	runGitForMindTest(t, repoDir, "push", "origin", "master", "feature/remote")
	runGitForMindTest(t, repoDir, "branch", "-D", "feature/remote")

	project, err := server.store.GetProject(projectID)
	if err != nil {
		t.Fatalf("failed to load project: %v", err)
	}
	project.Settings[projectWorktreesEnabledSettingKey] = "true"
	project.Settings[projectWorktreesDirectorySettingKey] = t.TempDir()
	project.Settings[projectWorktreesBaseRefSettingKey] = "master"
	if err := server.store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	// A new branch starts from the requested base instead of the project base ref.
	wt := createBranchTestWorktree(t, server, projectID, "feature/new", "feature/base")
	if wt.BaseRef != "feature/base" || wt.Branch != "feature/new" {
		t.Fatalf("unexpected worktree record: %+v", wt)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "base.txt")); err != nil {
		t.Fatalf("new branch did not start from the requested base: %v", err)
	}

	// An existing local branch is continued with its commits and survives the teardown.
	localHead, _ := runGitCommand(repoDir, "rev-parse", "feature/local")
	wt = createBranchTestWorktree(t, server, projectID, "feature/local", "")
	if head, _ := runGitCommand(wt.Path, "rev-parse", "HEAD"); head != localHead {
		t.Fatalf("continued branch HEAD = %s, want %s", head, localHead)
	}
	if rec := serveWorktreeTest(server, http.MethodDelete, "/sessions/"+wt.SessionID); rec.Code != http.StatusNoContent {
		t.Fatalf("delete failed: %d %s", rec.Code, rec.Body.String())
	}
	if head, _ := runGitCommand(repoDir, "rev-parse", "feature/local"); head != localHead {
		t.Fatalf("continued branch changed after teardown: %q", head)
	}

	// A remote-only branch gets a local tracking branch.
	wt = createBranchTestWorktree(t, server, projectID, "feature/remote", "")
	if upstream, _ := runGitCommand(wt.Path, "rev-parse", "--abbrev-ref", "@{upstream}"); upstream != "origin/feature/remote" {
		t.Fatalf("remote branch upstream = %q", upstream)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "remote.txt")); err != nil {
		t.Fatalf("remote branch content missing: %v", err)
	}

	// A branch open in the main folder cannot be checked out twice; the error names the folder.
	body, _ := json.Marshal(CreateSessionRequest{AgentID: "build", ProjectID: projectID, Branch: "master", Queued: true})
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(string(body))))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "branch master is open in ") ||
		!strings.Contains(rec.Body.String(), filepath.Base(repoDir)+"; switch that folder to another branch first") {
		t.Fatalf("expected an open-branch error, got %d %s", rec.Code, rec.Body.String())
	}
}

func createBranchTestWorktree(t *testing.T, server *Server, projectID string, branch string, baseRef string) *storage.SessionWorktree {
	t.Helper()
	body, _ := json.Marshal(CreateSessionRequest{AgentID: "build", ProjectID: projectID, Branch: branch, BaseRef: baseRef, Queued: true})
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(string(body))))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session %s failed: %d %s", branch, rec.Code, rec.Body.String())
	}
	var resp CreateSessionResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	wt, err := server.store.GetSessionWorktree(resp.ID)
	if err != nil || wt == nil {
		t.Fatalf("session %s has no worktree: %v", branch, err)
	}
	if current, _ := currentGitBranch(wt.Path); current != branch {
		t.Fatalf("worktree branch = %q, want %q", current, branch)
	}
	return wt
}
