package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestSessionWorktreesDisabledKeepMainFolder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)

	id := createWorktreeTestSession(t, server, projectID, "", http.StatusCreated)
	sess, err := server.sessionManager.Get(id)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if got := server.resolveSessionWorkDir(sess); got != repoDir {
		t.Fatalf("workdir = %q, want main folder %q", got, repoDir)
	}
	if body := getWorktreeTestSession(t, server, id); strings.Contains(body, `"worktree"`) {
		t.Fatalf("expected no worktree in session JSON: %s", body)
	}
}

func TestSessionWorktreesLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)
	worktreesDir := t.TempDir()
	hooksDir := t.TempDir()

	project, err := server.store.GetProject(projectID)
	if err != nil {
		t.Fatalf("failed to load project: %v", err)
	}
	project.Settings[projectWorktreesEnabledSettingKey] = "true"
	project.Settings[projectWorktreesDirectorySettingKey] = worktreesDir
	project.Settings[projectWorktreesBaseRefSettingKey] = "master"
	// Hooks write outside the worktree, an untracked file would block `git worktree remove`.
	project.Settings[projectWorktreesSetupCmdSettingKey] = `echo "$WORKTREE_SLOT $WORKTREE_NAME $WORKTREE_MAIN_ROOT" > ` + hooksDir + `/setup-$WORKTREE_SLOT`
	project.Settings[projectWorktreesTeardownCmdSettingKey] = `touch ` + hooksDir + `/teardown-$WORKTREE_NAME`
	project.Settings[projectWorktreesAppURLTemplateSettingKey] = "https://dev.example.com:300{slot}"
	if err := server.store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	// First session: requested branch, slot 1, setup hook gets the env contract.
	first := createWorktreeTestSession(t, server, projectID, "feature/one", http.StatusCreated)
	// The folder is named after the session, not the branch, so renaming the branch never moves it.
	firstName := "a2gent-" + first[:8]
	firstPath := filepath.Join(worktreesDir, firstName)
	if setup, _ := os.ReadFile(filepath.Join(hooksDir, "setup-1")); strings.TrimSpace(string(setup)) != "1 "+firstName+" "+repoDir {
		t.Fatalf("unexpected setup hook env: %q", setup)
	}
	var resp SessionResponse
	if err := json.Unmarshal([]byte(getWorktreeTestSession(t, server, first)), &resp); err != nil {
		t.Fatalf("failed to decode session: %v", err)
	}
	if resp.Worktree == nil || resp.Worktree.Path != firstPath || resp.Worktree.Branch != "feature/one" || resp.Worktree.BaseRef != "master" || resp.Worktree.Slot != 1 || resp.Worktree.AppURL != "https://dev.example.com:3001" {
		t.Fatalf("unexpected worktree payload: %+v", resp.Worktree)
	}
	firstSess, _ := server.sessionManager.Get(first)
	if got := server.resolveSessionWorkDir(firstSess); got != firstPath {
		t.Fatalf("workdir = %q, want worktree %q", got, firstPath)
	}
	if branch, _ := currentGitBranch(firstPath); branch != "feature/one" {
		t.Fatalf("worktree branch = %q", branch)
	}
	result, err := server.toolManagerForSession(firstSess).Execute(context.Background(), "bash", json.RawMessage(`{"command":"echo $WORKTREE_SLOT:$WORKTREE_NAME"}`))
	if err != nil || !strings.HasPrefix(result.Output, "1:"+firstName) {
		t.Fatalf("bash tool did not get the worktree env: %+v %v", result, err)
	}

	// Second session: default branch name, slot 2.
	second := createWorktreeTestSession(t, server, projectID, "", http.StatusCreated)
	secondWt, _ := server.store.GetSessionWorktree(second)
	if secondWt == nil || secondWt.Slot != 2 || secondWt.Branch != "a2gent/"+second[:8] {
		t.Fatalf("unexpected second worktree: %+v", secondWt)
	}
	// Completing a session commits all changes, but must wait for running descendants.
	child, err := server.sessionManager.CreateWithParent("build", first)
	if err != nil {
		t.Fatalf("failed to create child session: %v", err)
	}
	child.SetStatus(session.StatusRunning)
	if err := server.sessionManager.Save(child); err != nil {
		t.Fatalf("failed to save running child: %v", err)
	}
	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+first+"/archive"); rec.Code != http.StatusConflict {
		t.Fatalf("archive with a running child = %d, want conflict: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("running child worktree was removed: %v", err)
	}
	if archived, _ := server.sessionManager.Get(first); sessionArchivedAt(archived) != nil {
		t.Fatal("parent was archived despite its running child")
	}
	child.SetStatus(session.StatusCompleted)
	if err := server.sessionManager.Save(child); err != nil {
		t.Fatalf("failed to save completed child: %v", err)
	}
	writeGitTestFile(t, firstPath, "README.md", "completed work\n")
	writeGitTestFile(t, firstPath, "session-output.txt", "saved output\n")

	// Completing commits changes, then tears down: hook, folder removed, branch kept, slot reused.
	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+first+"/archive"); rec.Code != http.StatusOK {
		t.Fatalf("archive failed: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "teardown-"+firstName)); err != nil {
		t.Fatalf("teardown hook did not run: %v", err)
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("worktree folder still exists: %v", err)
	}
	runGitForMindTest(t, repoDir, "rev-parse", "--verify", "feature/one")
	if got, err := runGitCommand(repoDir, "show", "feature/one:README.md"); err != nil || got != "completed work" {
		t.Fatalf("committed tracked changes = %q, err = %v", got, err)
	}
	if got, err := runGitCommand(repoDir, "show", "feature/one:session-output.txt"); err != nil || got != "saved output" {
		t.Fatalf("committed untracked changes = %q, err = %v", got, err)
	}
	if got, err := runGitCommand(repoDir, "log", "-1", "--format=%s", "feature/one"); err != nil || got != "Complete session "+first[:8] {
		t.Fatalf("auto-commit message = %q, err = %v", got, err)
	}
	third := createWorktreeTestSession(t, server, projectID, "feature/three", http.StatusCreated)
	if wt, _ := server.store.GetSessionWorktree(third); wt == nil || wt.Slot != 1 {
		t.Fatalf("expected slot 1 to be reused: %+v", wt)
	}

	// Delete tears down as well.
	if rec := serveWorktreeTest(server, http.MethodDelete, "/sessions/"+second); rec.Code != http.StatusNoContent {
		t.Fatalf("delete failed: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(secondWt.Path); !os.IsNotExist(err) {
		t.Fatalf("deleted session worktree still exists: %v", err)
	}

	// Handoff needs a clean main folder, then moves the branch into it.
	writeGitTestFile(t, repoDir, "dirty.txt", "x\n")
	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+third+"/worktree/handoff"); rec.Code != http.StatusConflict {
		t.Fatalf("expected handoff to be refused on a dirty main folder, got %d", rec.Code)
	}
	runGitForMindTest(t, repoDir, "clean", "-f")
	thirdPath := filepath.Join(worktreesDir, "a2gent-"+third[:8])
	thirdSess, _ := server.sessionManager.Get(third)
	if prompt, _ := server.resolveEnvironmentContextSection(thirdSess); !strings.Contains(prompt, "git branch -m") || !strings.Contains(prompt, thirdPath) || !strings.Contains(prompt, "merges into the branch open in the project root (or the parent worktree) without pushing") {
		t.Fatalf("system prompt lacks worktree branch guidance: %s", prompt)
	}
	// The agent renames its branch: the payload and the handoff follow git, not the stored name.
	runGitForMindTest(t, thirdPath, "branch", "-m", "feature/renamed")
	if body := getWorktreeTestSession(t, server, third); !strings.Contains(body, `"branch":"feature/renamed"`) {
		t.Fatalf("payload did not pick up the renamed branch: %s", body)
	}
	// Detached HEAD shows the short commit and blocks the handoff.
	runGitForMindTest(t, thirdPath, "switch", "--detach")
	commit, _ := runGitCommand(thirdPath, "rev-parse", "--short", "HEAD")
	if body := getWorktreeTestSession(t, server, third); !strings.Contains(body, `"branch":"`+commit+`"`) {
		t.Fatalf("payload did not show the detached commit %s: %s", commit, body)
	}
	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+third+"/worktree/handoff"); rec.Code != http.StatusConflict {
		t.Fatalf("expected handoff to be refused on a detached HEAD, got %d", rec.Code)
	}
	runGitForMindTest(t, thirdPath, "switch", "feature/renamed")
	if rec := serveWorktreeTest(server, http.MethodPost, "/sessions/"+third+"/worktree/handoff"); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"worktree"`) {
		t.Fatalf("handoff failed: %d %s", rec.Code, rec.Body.String())
	}
	if branch, _ := currentGitBranch(repoDir); branch != "feature/renamed" {
		t.Fatalf("main folder branch = %q after handoff", branch)
	}
}

func TestConcurrentSessionWorktreeCreationSerializesGitMetadataUpdates(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)

	remoteDir := t.TempDir()
	runGitForMindTest(t, remoteDir, "init", "--bare")
	runGitForMindTest(t, repoDir, "remote", "add", "origin", remoteDir)
	runGitForMindTest(t, repoDir, "push", "-u", "origin", "master")

	project, err := server.store.GetProject(projectID)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	project.Settings[projectWorktreesEnabledSettingKey] = "true"
	project.Settings[projectWorktreesDirectorySettingKey] = t.TempDir()
	project.Settings[projectWorktreesBaseRefSettingKey] = "origin/master"
	if err := server.store.SaveProject(project); err != nil {
		t.Fatalf("save project settings: %v", err)
	}

	const count = 10
	sessionIDs := make([]string, count)
	for i := range sessionIDs {
		sess, err := server.sessionManager.CreateQueued("build")
		if err != nil {
			t.Fatalf("create queued session: %v", err)
		}
		sess.ProjectID = &projectID
		if err := server.sessionManager.Save(sess); err != nil {
			t.Fatalf("save queued session: %v", err)
		}
		sessionIDs[i] = sess.ID
	}

	errs := make(chan error, count)
	for _, id := range sessionIDs {
		go func(sessionID string) {
			errs <- server.createSessionWorktree(sessionID, projectID, "", "")
		}(id)
	}
	for i := 0; i < count; i++ {
		if err := <-errs; err != nil {
			t.Errorf("create session worktree concurrently: %v", err)
		}
	}

	slots := make(map[int]struct{}, count)
	for _, id := range sessionIDs {
		worktree, err := server.store.GetSessionWorktree(id)
		if err != nil {
			t.Fatalf("load worktree for %s: %v", id, err)
		}
		if worktree == nil {
			t.Errorf("session %s has no persisted worktree", id)
			continue
		}
		if _, exists := slots[worktree.Slot]; exists {
			t.Errorf("worktree slot %d was assigned more than once", worktree.Slot)
		}
		slots[worktree.Slot] = struct{}{}
	}
}

func createWorktreeTestSession(t *testing.T, server *Server, projectID string, branch string, wantStatus int) string {
	t.Helper()
	body, _ := json.Marshal(CreateSessionRequest{AgentID: "build", ProjectID: projectID, Branch: branch, Queued: true})
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(string(body))))
	if rec.Code != wantStatus {
		t.Fatalf("create session status = %d, want %d: %s", rec.Code, wantStatus, rec.Body.String())
	}
	var resp CreateSessionResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	return resp.ID
}

func getWorktreeTestSession(t *testing.T, server *Server, id string) string {
	t.Helper()
	rec := serveWorktreeTest(server, http.MethodGet, "/sessions/"+id)
	if rec.Code != http.StatusOK {
		t.Fatalf("get session failed: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func serveWorktreeTest(server *Server, method string, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}
