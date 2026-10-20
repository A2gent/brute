package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newLocalCompletionTestSession(t *testing.T, branch string) (*Server, string, string, string) {
	t.Helper()
	server, projectID, root := newProjectFileTestServer(t)
	initMindGitTestRepo(t, root)
	runGitForMindTest(t, root, "branch", "-M", branch)
	project, err := server.store.GetProject(projectID)
	if err != nil {
		t.Fatal(err)
	}
	project.Settings[projectWorktreesEnabledSettingKey] = "true"
	project.Settings[projectWorktreesDirectorySettingKey] = t.TempDir()
	project.Settings[projectWorktreesBaseRefSettingKey] = branch
	if err := server.store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	id := createWorktreeTestSession(t, server, projectID, "", http.StatusCreated)
	wt, err := server.store.GetSessionWorktree(id)
	if err != nil || wt == nil {
		t.Fatalf("worktree: %+v, %v", wt, err)
	}
	return server, id, root, wt.Path
}

func assertCompletionKeptSession(t *testing.T, server *Server, id, path string) {
	t.Helper()
	sess, err := server.sessionManager.Get(id)
	if err != nil || sessionArchivedAt(sess) != nil {
		t.Fatalf("failed completion archived session: %v, %v", sess, err)
	}
	wt, err := server.store.GetSessionWorktree(id)
	if err != nil || wt == nil {
		t.Fatalf("failed completion lost worktree record: %v, %v", wt, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("failed completion removed worktree: %v", err)
	}
}

func TestCompleteMergesIntoLocalBranchWithoutPush(t *testing.T) {
	for _, branch := range []string{"main", "master"} {
		t.Run(branch, func(t *testing.T) {
			server, id, root, path := newLocalCompletionTestSession(t, branch)
			// A local bare remote proves Complete neither needs nor advances origin.
			remote := t.TempDir()
			runGitForMindTest(t, remote, "init", "--bare")
			runGitForMindTest(t, root, "remote", "add", "origin", remote)
			runGitForMindTest(t, root, "push", "origin", branch)
			before, _ := runGitCommand(remote, "rev-parse", "refs/heads/"+branch)
			writeGitTestFile(t, path, "delivered.txt", "visible in the running checkout\n")
			// Renaming must not break completion's use of the actual checkout HEAD.
			runGitForMindTest(t, path, "branch", "-m", "renamed-session")
			if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive"); rec.Code != http.StatusOK {
				t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
			}
			contents, err := os.ReadFile(filepath.Join(root, "delivered.txt"))
			if err != nil || string(contents) != "visible in the running checkout\n" {
				t.Fatalf("main checkout did not receive changes: %q %v", contents, err)
			}
			if _, err := runGitCommand(root, "merge-base", "--is-ancestor", "renamed-session", branch); err != nil {
				t.Fatalf("session is not in local %s: %v", branch, err)
			}
			after, _ := runGitCommand(remote, "rev-parse", "refs/heads/"+branch)
			if before != after {
				t.Fatal("Complete pushed to origin")
			}
			if current, _ := currentGitBranch(root); current != branch {
				t.Fatalf("main folder switched branch: %s", current)
			}
		})
	}
}

func TestCompleteMergesDivergedLocalHistory(t *testing.T) {
	server, id, root, path := newLocalCompletionTestSession(t, "main")
	writeGitTestFile(t, root, "human.txt", "local main work\n")
	runGitForMindTest(t, root, "add", "human.txt")
	runGitForMindTest(t, root, "commit", "-m", "Human work")
	local, _ := runGitCommand(root, "rev-parse", "HEAD")
	writeGitTestFile(t, path, "agent.txt", "session work\n")
	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive"); rec.Code != http.StatusOK {
		t.Fatalf("complete diverged branches: %d %s", rec.Code, rec.Body.String())
	}
	for _, file := range []string{"human.txt", "agent.txt"} {
		if _, err := os.Stat(filepath.Join(root, file)); err != nil {
			t.Fatalf("lost %s: %v", file, err)
		}
	}
	if _, err := runGitCommand(root, "merge-base", "--is-ancestor", local, "HEAD"); err != nil {
		t.Fatalf("lost human commit: %v", err)
	}
}

func TestCompleteRejectsDirtyMainAndCanRetry(t *testing.T) {
	for _, staged := range []bool{false, true} {
		t.Run(map[bool]string{false: "untracked", true: "staged"}[staged], func(t *testing.T) {
			server, id, root, path := newLocalCompletionTestSession(t, "main")
			writeGitTestFile(t, root, "human.txt", "do not touch\n")
			if staged {
				runGitForMindTest(t, root, "add", "human.txt")
			}
			before, _ := runGitCommand(root, "status", "--porcelain")
			writeGitTestFile(t, path, "agent.txt", "session work\n")
			rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive")
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "uncommitted changes") {
				t.Fatalf("dirty main: %d %s", rec.Code, rec.Body.String())
			}
			assertCompletionKeptSession(t, server, id, path)
			after, _ := runGitCommand(root, "status", "--porcelain")
			if before != after {
				t.Fatalf("changed main index or files: before=%q after=%q", before, after)
			}
			runGitForMindTest(t, root, "add", "human.txt")
			runGitForMindTest(t, root, "commit", "-m", "Preserve human work")
			if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive"); rec.Code != http.StatusOK {
				t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCompleteAbortsConflictingMergeAndKeepsWorktree(t *testing.T) {
	server, id, root, path := newLocalCompletionTestSession(t, "main")
	writeGitTestFile(t, root, "README.md", "human version\n")
	runGitForMindTest(t, root, "add", "README.md")
	runGitForMindTest(t, root, "commit", "-m", "Human version")
	before, _ := runGitCommand(root, "rev-parse", "HEAD")
	writeGitTestFile(t, path, "README.md", "agent version\n")
	rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "merge failed") {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body.String())
	}
	assertCompletionKeptSession(t, server, id, path)
	after, _ := runGitCommand(root, "rev-parse", "HEAD")
	status, _ := runGitCommand(root, "status", "--porcelain")
	if before != after || status != "" {
		t.Fatalf("failed merge changed main: before=%s after=%s status=%s", before, after, status)
	}
	if _, err := runGitCommand(root, "rev-parse", "--verify", "MERGE_HEAD"); err == nil {
		t.Fatal("failed completion left a merge in progress")
	}
}

func TestCompleteRespectsCommitHookFailure(t *testing.T) {
	server, id, root, path := newLocalCompletionTestSession(t, "main")
	hooks := t.TempDir()
	runGitForMindTest(t, root, "config", "core.hooksPath", hooks)
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\necho rejected-by-test >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitTestFile(t, path, "agent.txt", "needs verification\n")
	rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "rejected-by-test") {
		t.Fatalf("hook failure: %d %s", rec.Code, rec.Body.String())
	}
	assertCompletionKeptSession(t, server, id, path)
	if _, err := os.Stat(filepath.Join(root, "agent.txt")); !os.IsNotExist(err) {
		t.Fatalf("hook failure delivered unverified work: %v", err)
	}
}

func TestCompleteMissingWorktreeDeliversSurvivingBranch(t *testing.T) {
	server, id, root, path := newLocalCompletionTestSession(t, "main")
	writeGitTestFile(t, path, "agent.txt", "committed before removal\n")
	runGitForMindTest(t, path, "add", "agent.txt")
	runGitForMindTest(t, path, "commit", "-m", "Preserve session work")
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive"); rec.Code != http.StatusOK {
		t.Fatalf("missing worktree: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "agent.txt")); err != nil {
		t.Fatalf("archived without delivering surviving branch: %v", err)
	}
}

func TestDeleteDoesNotMergeSession(t *testing.T) {
	server, id, root, path := newLocalCompletionTestSession(t, "main")
	writeGitTestFile(t, path, "agent.txt", "not accepted\n")
	runGitForMindTest(t, path, "add", "agent.txt")
	runGitForMindTest(t, path, "commit", "-m", "Unaccepted work")
	before, _ := runGitCommand(root, "rev-parse", "HEAD")
	if rec := serveWorktreeTest(server, http.MethodDelete, "/sessions/"+id); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := runGitCommand(root, "rev-parse", "HEAD")
	if before != after {
		t.Fatal("Delete merged unaccepted work")
	}
}

// Complete merges into whatever branch the human has open, not only main/master.
func TestCompleteMergesIntoCurrentBranch(t *testing.T) {
	server, id, root, path := newLocalCompletionTestSession(t, "main")
	runGitForMindTest(t, root, "switch", "-c", "human-work")
	writeGitTestFile(t, path, "agent.txt", "session work\n")
	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive"); rec.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	if current, _ := currentGitBranch(root); current != "human-work" {
		t.Fatalf("changed human branch: %s", current)
	}
	if _, err := os.Stat(filepath.Join(root, "agent.txt")); err != nil {
		t.Fatalf("not merged into current branch: %v", err)
	}
	if _, err := runGitCommand(root, "cat-file", "-e", "main:agent.txt"); err == nil {
		t.Fatal("work leaked into main")
	}
}

func TestCompletePreviewAndKeepMode(t *testing.T) {
	server, id, root, path := newLocalCompletionTestSession(t, "main")
	preview := func() completePreviewResponse {
		rec := serveWorktreeTest(server, http.MethodGet, "/sessions/"+id+"/complete-preview")
		var resp completePreviewResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil {
			t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
		}
		return resp
	}
	if p := preview(); !p.HasWorktree || p.HasChanges || p.TargetBranch != "main" {
		t.Fatalf("clean preview: %+v", p)
	}
	writeGitTestFile(t, path, "agent.txt", "session work\n")
	if p := preview(); !p.HasChanges {
		t.Fatalf("dirty preview: %+v", p)
	}
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+id+"/archive", strings.NewReader(`{"mode":"keep"}`))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("keep: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "agent.txt")); err == nil {
		t.Fatal("keep mode merged into the main folder")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("worktree was not removed")
	}
}

func TestCompleteAlreadyIntegratedAllowsDirtyMain(t *testing.T) {
	server, id, root, path := newLocalCompletionTestSession(t, "main")
	writeGitTestFile(t, path, "agent.txt", "already delivered\n")
	runGitForMindTest(t, path, "add", "agent.txt")
	runGitForMindTest(t, path, "commit", "-m", "Delivered work")
	commit, _ := runGitCommand(path, "rev-parse", "HEAD")
	runGitForMindTest(t, root, "merge", "--ff-only", commit)
	writeGitTestFile(t, root, "human.txt", "keep this WIP\n")
	before, _ := runGitCommand(root, "status", "--porcelain")
	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive"); rec.Code != http.StatusOK {
		t.Fatalf("already integrated: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := runGitCommand(root, "status", "--porcelain")
	if before != after {
		t.Fatalf("touched dirty main: before=%q after=%q", before, after)
	}
}

func TestCompleteMergesDetachedSessionCommit(t *testing.T) {
	server, id, root, path := newLocalCompletionTestSession(t, "main")
	runGitForMindTest(t, path, "switch", "--detach")
	writeGitTestFile(t, path, "agent.txt", "detached session work\n")
	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive"); rec.Code != http.StatusOK {
		t.Fatalf("detached completion: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "agent.txt")); err != nil {
		t.Fatalf("lost detached commit: %v", err)
	}
}

// With both the folder and the branch gone there is nothing to deliver; Complete must still archive.
func TestCompleteMissingWorktreeAndBranchArchives(t *testing.T) {
	server, id, root, path := newLocalCompletionTestSession(t, "main")
	branch, _ := currentGitBranch(path)
	runGitForMindTest(t, root, "worktree", "remove", path)
	runGitForMindTest(t, root, "branch", "-d", branch)
	rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+id+"/archive")
	if rec.Code != http.StatusOK {
		t.Fatalf("missing source: %d %s", rec.Code, rec.Body.String())
	}
	sess, _ := server.sessionManager.Get(id)
	wt, _ := server.store.GetSessionWorktree(id)
	if sessionArchivedAt(sess) == nil || wt != nil {
		t.Fatal("session was not archived after worktree and branch disappeared")
	}
}
