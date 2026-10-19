package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// The sessions list and catalog rows must carry the same worktree fields as GET /sessions/{id},
// otherwise Caesar's list chips never render.
func TestSessionListWorktreeFields(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	project, err := server.store.GetProject(projectID)
	if err != nil {
		t.Fatalf("failed to load project: %v", err)
	}
	project.Settings[projectWorktreesEnabledSettingKey] = "true"
	project.Settings[projectWorktreesAppURLTemplateSettingKey] = "https://dev.example.com:300{slot}"
	if err := server.store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}
	owner, _ := server.sessionManager.Create("build")
	child, _ := server.sessionManager.CreateWithParent("build", owner.ID)
	legacy, _ := server.sessionManager.Create("build")
	for _, id := range []string{owner.ID, child.ID, legacy.ID} {
		sess, _ := server.sessionManager.Get(id)
		sess.ProjectID = &projectID
		if err := server.sessionManager.Save(sess); err != nil {
			t.Fatalf("failed to save session: %v", err)
		}
	}
	if err := server.store.CreateSessionWorktree(&storage.SessionWorktree{
		SessionID: owner.ID, ProjectID: projectID, Path: "/tmp/wt-owner", Branch: "a2gent/owner", BaseRef: "master", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("failed to store worktree: %v", err)
	}

	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sessions?project_id="+projectID, nil))
	var items []SessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("failed to decode list %d: %s", rec.Code, rec.Body.String())
	}
	byID := map[string]SessionListItem{}
	for _, item := range items {
		byID[item.ID] = item
	}
	for _, id := range []string{owner.ID, child.ID} {
		wt := byID[id].Worktree
		if wt == nil || wt.Branch != "a2gent/owner" || wt.Slot != 1 || wt.AppURL != "https://dev.example.com:3001" || byID[id].MainFolder {
			t.Fatalf("session %s: unexpected worktree fields %+v main_folder=%v", id, wt, byID[id].MainFolder)
		}
	}
	if byID[legacy.ID].Worktree != nil || !byID[legacy.ID].MainFolder {
		t.Fatalf("legacy session should be marked main folder: %+v", byID[legacy.ID])
	}

	childSess, _ := server.sessionManager.Get(child.ID)
	if wt, mainFolder := server.sessionWorktreeFields(childSess); wt == nil || wt.Branch != "a2gent/owner" || mainFolder {
		t.Fatalf("catalog/detail fields differ from the list: %+v %v", wt, mainFolder)
	}
}
