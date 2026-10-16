package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func assertResourceEvent(t *testing.T, events <-chan SessionCatalogEvent, resource, projectID string) SessionCatalogEvent {
	t.Helper()
	for {
		event := readCatalogEvent(t, events)
		if event.Type != "project_resources_changed" {
			continue
		}
		if event.Resource != resource || event.ProjectID != projectID || event.SessionID != "" || event.Session != nil {
			t.Fatalf("resource event = %+v, want %s/%s without session payload", event, resource, projectID)
		}
		return event
	}
}

func TestProjectResourceEventsScopeAndReplay(t *testing.T) {
	server := &Server{}
	hub := server.catalogHub()
	project := hub.subscribe("project-a", "", true)
	defer hub.unsubscribe(project)
	cursor := readCatalogEvent(t, project.events).ID
	server.publishProjectResourcesChanged("jobs", "project-b")
	server.publishProjectResourcesChanged("jobs", "project-a", "project-a")
	scoped := assertResourceEvent(t, project.events, "jobs", "project-a")
	server.publishProjectResourcesChanged("mcp", "")
	global := assertResourceEvent(t, project.events, "mcp", "")

	replay := hub.subscribe("project-a", cursor, true)
	defer hub.unsubscribe(replay)
	if got := assertResourceEvent(t, replay.events, "jobs", "project-a"); got.ID != scoped.ID {
		t.Fatalf("replayed id = %s, want %s", got.ID, scoped.ID)
	}
	if got := assertResourceEvent(t, replay.events, "mcp", ""); got.ID != global.ID {
		t.Fatalf("replayed global id = %s, want %s", got.ID, global.ID)
	}
	if got := readCatalogEvent(t, replay.events); got.Type != "catalog_ready" {
		t.Fatalf("duplicate resource event: %+v", got)
	}
	if len(hub.changed) != 0 || len(hub.lastPayload) != 0 {
		t.Fatal("resource changes polluted session delta tracking")
	}
}

func TestProjectResourceHTTPMutations(t *testing.T) {
	oldRunCommand := runCommand
	runCommand = func(context.Context, string, ...string) (string, error) { return "", nil }
	t.Cleanup(func() { runCommand = oldRunCommand })
	for _, resource := range []string{"databases", "mcp", "teams", "agents"} {
		t.Run(resource, func(t *testing.T) {
			server, store := newUnifiedAgentsTestServer(t)
			folder := t.TempDir()
			projectID := "project-resources"
			now := time.Now()
			if err := store.SaveProject(&storage.Project{ID: projectID, Name: "Resources", Folder: &folder, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			events, unsubscribe := server.SubscribeSessionCatalogEvents(projectID)
			defer unsubscribe()
			var path string
			var create, update map[string]interface{}
			switch resource {
			case "databases":
				path = "/projects/" + projectID + "/databases"
				create = map[string]interface{}{"name": "DB", "engine": "sqlite", "dsn": ":memory:", "environment": "local"}
				update = map[string]interface{}{"name": "Updated DB"}
			case "mcp":
				path = "/mcp/servers"
				create = map[string]interface{}{"name": "MCP", "project_id": projectID, "transport": "http", "url": "http://localhost:8080/mcp"}
				update = create
			case "teams":
				path = "/teams"
				create = map[string]interface{}{"project_id": projectID, "yaml": teamsAPIValidYAML}
				update = create
			case "agents":
				path = "/sub-agents"
				create = map[string]interface{}{"project_id": projectID, "name": "Reviewer"}
				update = create
			}
			created := requestTeamJSON(t, server, http.MethodPost, path, create)
			if created.Code != http.StatusCreated {
				t.Fatalf("POST %s = %d: %s", path, created.Code, created.Body.String())
			}
			assertResourceEvent(t, events, resource, projectID)
			var record struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(created.Body.Bytes(), &record); err != nil || record.ID == "" {
				t.Fatalf("create response = %s, error = %v", created.Body.String(), err)
			}
			path += "/" + record.ID
			updated := requestTeamJSON(t, server, http.MethodPut, path, update)
			if updated.Code != http.StatusOK {
				t.Fatalf("PUT %s = %d: %s", path, updated.Code, updated.Body.String())
			}
			assertResourceEvent(t, events, resource, projectID)
			deleted := requestTeamJSON(t, server, http.MethodDelete, path, nil)
			if deleted.Code != http.StatusNoContent && deleted.Code != http.StatusOK {
				t.Fatalf("DELETE %s = %d: %s", path, deleted.Code, deleted.Body.String())
			}
			deleteProject := projectID
			if resource == "agents" {
				deleteProject = "" // Definition/container cleanup may affect multiple scopes.
			}
			assertResourceEvent(t, events, resource, deleteProject)
		})
	}
}

func TestRecurringJobsToolPublishesResourceChanges(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"cron\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"0 0 * * *\"}]}]}}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	server, sess := newCatalogStartTestServer(t, provider.URL, "")
	folder := t.TempDir()
	projectID := "project-tool"
	now := time.Now()
	if err := server.store.SaveProject(&storage.Project{ID: projectID, Name: "Tool", Folder: &folder, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	sess.ProjectID = &projectID
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := server.SubscribeSessionCatalogEvents(projectID)
	defer unsubscribe()
	tool := newRecurringJobsTool(server)
	ctx := context.WithValue(context.Background(), "session_id", sess.ID)
	result, err := tool.Execute(ctx, json.RawMessage(`{"action":"create","name":"External loop","schedule_text":"daily","task_prompt":"Do work"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal(result.Error)
	}
	assertResourceEvent(t, events, "jobs", projectID)
	allJobs, err := server.store.ListJobs()
	if err != nil {
		t.Fatal(err)
	}
	var jobs []*storage.RecurringJob
	for _, job := range allJobs {
		if jobMatchesProject(job, projectID) {
			jobs = append(jobs, job)
		}
	}
	if len(jobs) != 1 {
		t.Fatalf("persisted project jobs = %+v", jobs)
	}
	params, _ := json.Marshal(map[string]interface{}{"action": "delete", "job_id": jobs[0].ID})
	result, err = tool.Execute(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal(result.Error)
	}
	assertResourceEvent(t, events, "jobs", projectID)
}

func TestJobExecutionChangesPublishWithoutHTTPMutation(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	job := &storage.RecurringJob{ID: "loop", Name: "Loop", ScheduleCron: "* * * * *", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := server.SubscribeSessionCatalogEvents("")
	defer unsubscribe()
	exec := &storage.JobExecution{ID: "run", JobID: job.ID, Status: "running", StartedAt: time.Now()}
	if err := server.saveJobExecution(exec); err != nil {
		t.Fatal(err)
	}
	assertResourceEvent(t, events, "jobs", "")
	server.failJobExecution(exec, nil, "failed before agent work")
	assertResourceEvent(t, events, "jobs", "")
	if _, err := server.recordSkippedJobExecution(job, "nothing actionable"); err != nil {
		t.Fatal(err)
	}
	assertResourceEvent(t, events, "jobs", "")
	exec.Status = "success"
	if err := server.saveJobExecution(exec); err != nil {
		t.Fatal(err)
	}
	assertResourceEvent(t, events, "jobs", "")
}

func TestJobAndMCPMovesInvalidateBothProjects(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	for _, id := range []string{"old", "new"} {
		if err := store.SaveProject(&storage.Project{ID: id, Name: id, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	oldID, newID := "old", "new"
	job := &storage.RecurringJob{ID: "loop", ProjectID: &oldID, Name: "Loop", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	mcp := &storage.MCPServer{ID: "mcp", ProjectID: &oldID, Name: "MCP", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMCPServer(mcp); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := server.SubscribeSessionCatalogEvents("")
	defer unsubscribe()
	job.ProjectID, mcp.ProjectID = &newID, &newID
	if err := server.saveJob(job); err != nil {
		t.Fatal(err)
	}
	assertResourceEvent(t, events, "jobs", newID)
	assertResourceEvent(t, events, "jobs", oldID)
	if err := server.saveMCPServer(mcp); err != nil {
		t.Fatal(err)
	}
	assertResourceEvent(t, events, "mcp", newID)
	assertResourceEvent(t, events, "mcp", oldID)
}

func TestFailedResourceWritesDoNotPublish(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	_ = store.Close()
	if err := server.saveJob(&storage.RecurringJob{ID: "loop"}); err == nil {
		t.Fatal("expected write failure")
	}
	if err := server.saveMCPServer(&storage.MCPServer{ID: "mcp"}); err == nil {
		t.Fatal("expected write failure")
	}
	if err := server.deleteJob("loop"); err == nil {
		t.Fatal("expected delete failure")
	}
	if err := server.deleteMCPServer("mcp"); err == nil {
		t.Fatal("expected delete failure")
	}
	if err := server.saveJobExecution(&storage.JobExecution{ID: "run"}); err == nil {
		t.Fatal("expected execution write failure")
	}
	if len(server.catalogHub().ring) != 0 {
		t.Fatal("failed write published a resource event")
	}
}

func TestDatabaseDeleteInvalidatesActualOwner(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	owner := "database-owner"
	if err := store.SaveProject(&storage.Project{ID: owner, Name: owner, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProjectDatabase(&storage.ProjectDatabase{ID: "database", ProjectID: owner, Name: "DB", Engine: "sqlite", DSN: ":memory:", Environment: "local", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := server.SubscribeSessionCatalogEvents(owner)
	defer unsubscribe()
	response := requestTeamJSON(t, server, http.MethodDelete, "/projects/not-the-owner/databases/database", nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", response.Code, response.Body.String())
	}
	assertResourceEvent(t, events, "databases", owner)
}

func TestAgentDefinitionToolPublishesResourceChange(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	events, unsubscribe := server.SubscribeSessionCatalogEvents("any-project")
	defer unsubscribe()
	params, _ := json.Marshal(map[string]string{"config_yaml": "version: \"1\"\nagent:\n  id: events-reviewer\n  name: Reviewer\nruntime:\n  type: docker\nworkspace:\n  scope: current_project\n  mount: rw\n"})
	result, err := newImportAgentDefinitionYAMLTool(server).Execute(context.Background(), params)
	if err != nil || !result.Success {
		t.Fatalf("import = %+v, err=%v", result, err)
	}
	assertResourceEvent(t, events, "agents", "")
}
