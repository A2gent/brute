package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
)

func TestProjectWorktreesCreateListAndRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)
	worktreesDir := t.TempDir()

	request := func(method string, target string, body string) ([]ProjectWorktree, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		server.router.ServeHTTP(rec, httptest.NewRequest(method, "/projects/"+projectID+"/worktrees"+target, strings.NewReader(body)))
		var worktrees []ProjectWorktree
		if rec.Code < 300 && rec.Code != http.StatusNoContent {
			if err := json.NewDecoder(rec.Body).Decode(&worktrees); err != nil {
				t.Fatalf("failed to decode worktrees: %v", err)
			}
		}
		return worktrees, rec
	}

	if _, rec := request(http.MethodPost, "", `{"name":"review"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected creation to be rejected while disabled, got %d", rec.Code)
	}

	project, err := server.store.GetProject(projectID)
	if err != nil {
		t.Fatalf("failed to load project: %v", err)
	}
	project.Settings[projectWorktreesEnabledSettingKey] = "true"
	project.Settings[projectWorktreesDirectorySettingKey] = worktreesDir
	project.Settings[projectWorktreesBaseRefSettingKey] = "master"
	if err := server.store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	worktrees, rec := request(http.MethodPost, "", `{"name":"review"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected worktree to be created, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(worktrees) != 2 || !worktrees[0].Main || worktrees[0].Branch != "master" || !worktrees[1].Detached || !strings.HasSuffix(worktrees[1].Path, "/review") {
		t.Fatalf("unexpected worktrees after create: %+v", worktrees)
	}

	writeGitTestFile(t, worktrees[1].Path, "draft.txt", "wip\n")
	worktrees, _ = request(http.MethodGet, "", "")
	if worktrees[1].Changes != 1 || !strings.HasPrefix(worktrees[1].LastCommit, "initial (") {
		t.Fatalf("expected activity summary for the new worktree: %+v", worktrees[1])
	}
	runGitForMindTest(t, worktrees[1].Path, "clean", "-f")

	worktrees, rec = request(http.MethodDelete, "?path="+url.QueryEscape(worktrees[1].Path), "")
	if rec.Code != http.StatusNoContent || len(worktrees) != 0 {
		t.Fatalf("expected worktree removal acknowledgement without a redundant list, got %d: %s", rec.Code, rec.Body.String())
	}

	worktrees, rec = request(http.MethodPost, "", `{"name":"dirty"}`)
	if rec.Code != http.StatusCreated || len(worktrees) != 2 {
		t.Fatalf("expected dirty-test worktree to be created, got %d: %s", rec.Code, rec.Body.String())
	}
	dirtyPath := worktrees[1].Path
	writeGitTestFile(t, dirtyPath, "draft.txt", "wip\n")

	_, rec = request(http.MethodDelete, "?path="+url.QueryEscape(dirtyPath), "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected dirty worktree removal to require force, got %d: %s", rec.Code, rec.Body.String())
	}
	worktrees, rec = request(http.MethodGet, "", "")
	if rec.Code != http.StatusOK || len(worktrees) != 2 || worktrees[1].Changes != 1 {
		t.Fatalf("expected dirty worktree to remain after regular removal, got %d: %+v", rec.Code, worktrees)
	}

	// Switching worktrees off keeps the extension while a worktree still needs it,
	// removing the last one then drops it so older git can read the repository again.
	project.Settings[projectWorktreesEnabledSettingKey] = "false"
	if err := server.store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}
	_, rec = request(http.MethodDelete, "?path="+url.QueryEscape(dirtyPath)+"&force=true", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected forced removal of dirty worktree, got %d: %s", rec.Code, rec.Body.String())
	}
	if value, err := runGitCommand(repoDir, "config", "--get", "extensions.relativeworktrees"); err == nil {
		t.Fatalf("expected relativeworktrees extension to be dropped, got %q", value)
	}
	worktrees, rec = request(http.MethodGet, "", "")
	if rec.Code != http.StatusOK || len(worktrees) != 1 || !worktrees[0].Main {
		t.Fatalf("expected only main worktree to remain after forced removal, got %d: %+v", rec.Code, worktrees)
	}
}
