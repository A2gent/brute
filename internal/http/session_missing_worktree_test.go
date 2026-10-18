package http

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestArchiveSessionWhenWorktreeFolderIsMissing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)

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

	id := createWorktreeTestSession(t, server, projectID, "feature/missing", http.StatusCreated)
	wt, err := server.store.GetSessionWorktree(id)
	if err != nil || wt == nil {
		t.Fatalf("failed to load session worktree: %+v, %v", wt, err)
	}
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatalf("failed to remove worktree folder: %v", err)
	}

	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive"); rec.Code != http.StatusOK {
		t.Fatalf("archive with a missing worktree = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	sess, err := server.sessionManager.Get(id)
	if err != nil || sessionArchivedAt(sess) == nil {
		t.Fatalf("session was not archived: session=%+v err=%v", sess, err)
	}
	if wt, err := server.store.GetSessionWorktree(id); err != nil || wt != nil {
		t.Fatalf("stale worktree record was not removed: worktree=%+v err=%v", wt, err)
	}
}

func TestArchiveSessionWhenWorktreePathIsNotGitWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)

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

	id := createWorktreeTestSession(t, server, projectID, "feature/not-a-worktree", http.StatusCreated)
	wt, err := server.store.GetSessionWorktree(id)
	if err != nil || wt == nil {
		t.Fatalf("failed to load session worktree: %+v, %v", wt, err)
	}
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatalf("remove original worktree: %v", err)
	}
	if err := os.MkdirAll(wt.Path, 0o755); err != nil {
		t.Fatalf("create plain directory at worktree path: %v", err)
	}
	marker := filepath.Join(wt.Path, "preserve.txt")
	if err := os.WriteFile(marker, []byte("preserve this directory"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive"); rec.Code != http.StatusOK {
		t.Fatalf("archive with a non-worktree path = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	remaining, err := server.store.GetSessionWorktree(id)
	if err != nil {
		t.Fatalf("check stale worktree record: %v", err)
	}
	if remaining != nil {
		t.Fatalf("stale worktree record remains: %+v", remaining)
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "preserve this directory" {
		t.Fatalf("non-worktree directory was not preserved: contents=%q err=%v", contents, err)
	}
}
