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
		if rec.Code < 300 {
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

	worktrees, rec = request(http.MethodDelete, "?path="+url.QueryEscape(worktrees[1].Path), "")
	if rec.Code != http.StatusOK || len(worktrees) != 1 {
		t.Fatalf("expected worktree to be removed, got %d: %s", rec.Code, rec.Body.String())
	}
}
