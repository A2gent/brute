package http

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

// Matches the plain string key the agent loop uses when injecting the session into tool contexts.
const sessionIDContextKey = "session_id"

func newTasksToolFixture(t *testing.T) (*tasksTool, context.Context, *storage.SQLiteStore, string) {
	t.Helper()
	server, store := newProjectsAPITestServer(t)
	t.Cleanup(func() { store.Close() })

	root := t.TempDir()
	project := &storage.Project{ID: "proj-tasks", Name: "A2 Gent", Folder: &root}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject() error = %v", err)
	}
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("Create session error = %v", err)
	}
	sess.ProjectID = &project.ID
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatalf("Save session error = %v", err)
	}
	ctx := context.WithValue(context.Background(), sessionIDContextKey, sess.ID)
	return newTasksTool(server), ctx, store, project.ID
}

func runTasksTool(t *testing.T, tool *tasksTool, ctx context.Context, params map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(params)
	result, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute(%v) error = %v", params, err)
	}
	if !result.Success {
		t.Fatalf("Execute(%v) failed: %s", params, result.Error)
	}
	return result.Output
}

func TestTasksToolCreateListAndStats(t *testing.T) {
	tool, ctx, store, projectID := newTasksToolFixture(t)

	output := runTasksTool(t, tool, ctx, map[string]any{
		"action": "create", "title": "Fix login redirect", "status": "todo",
		"priority": 1, "complexity": 2, "tags": []string{"auth"},
	})
	if !strings.Contains(output, "AG-1") || !strings.Contains(output, "Fix login redirect") {
		t.Fatalf("create output = %q, want assigned ref and title", output)
	}
	tasks, err := store.ListTasks(projectID)
	if err != nil || len(tasks) != 1 || tasks[0].CreatedBy != "agent" {
		t.Fatalf("stored tasks = %#v, err = %v", tasks, err)
	}

	runTasksTool(t, tool, ctx, map[string]any{"action": "create", "title": "Write docs", "status": "done"})

	listed := runTasksTool(t, tool, ctx, map[string]any{"action": "list", "status": "todo"})
	if !strings.Contains(listed, "AG-1 [todo p1 c2] Fix login redirect") {
		t.Fatalf("list output = %q, want compact todo line", listed)
	}
	if strings.Contains(listed, "Write docs") {
		t.Fatalf("list output = %q, want status filter applied", listed)
	}

	stats := runTasksTool(t, tool, ctx, map[string]any{"action": "stats"})
	if !strings.Contains(stats, "todo 1") || !strings.Contains(stats, "done 1") || !strings.Contains(stats, "total 2") {
		t.Fatalf("stats output = %q", stats)
	}
}

func TestTasksToolNextPicksHighestPriorityOpenTask(t *testing.T) {
	tool, ctx, _, _ := newTasksToolFixture(t)

	runTasksTool(t, tool, ctx, map[string]any{"action": "create", "title": "Low prio", "priority": 4})
	runTasksTool(t, tool, ctx, map[string]any{"action": "create", "title": "Urgent one", "priority": 0, "body": "detail body"})
	runTasksTool(t, tool, ctx, map[string]any{"action": "create", "title": "Already done", "priority": 0, "status": "done"})
	runTasksTool(t, tool, ctx, map[string]any{"action": "create", "title": "Taken", "priority": 0, "status": "in_progress"})

	output := runTasksTool(t, tool, ctx, map[string]any{"action": "next"})
	if !strings.Contains(output, "Urgent one") {
		t.Fatalf("next output = %q, want highest priority open task", output)
	}
	if !strings.Contains(output, "detail body") {
		t.Fatalf("next output = %q, want task body included", output)
	}

	// claim=true is the "take the next task" primitive: pick and move to in_progress in one call.
	claimed := runTasksTool(t, tool, ctx, map[string]any{"action": "next", "claim": true})
	if !strings.Contains(claimed, "in_progress") || !strings.Contains(claimed, "Urgent one") {
		t.Fatalf("claim output = %q", claimed)
	}
	after := runTasksTool(t, tool, ctx, map[string]any{"action": "next"})
	if strings.Contains(after, "Urgent one") {
		t.Fatalf("next after claim = %q, want a different task", after)
	}
}

func TestTasksToolDependenciesBlockNextUntilPrerequisitesAreDone(t *testing.T) {
	tool, ctx, _, _ := newTasksToolFixture(t)

	runTasksTool(t, tool, ctx, map[string]any{"action": "create", "title": "Foundation", "priority": 3})
	created := runTasksTool(t, tool, ctx, map[string]any{
		"action": "create", "title": "Feature", "priority": 0, "depends_on": []string{"AG-1"},
	})
	if !strings.Contains(created, "depends on AG-1") {
		t.Fatalf("create output = %q, want dependency refs", created)
	}
	if next := runTasksTool(t, tool, ctx, map[string]any{"action": "next"}); !strings.Contains(next, "Foundation") {
		t.Fatalf("next = %q, want unblocked prerequisite", next)
	}
	runTasksTool(t, tool, ctx, map[string]any{"action": "update", "ref": "AG-1", "status": "done"})
	if next := runTasksTool(t, tool, ctx, map[string]any{"action": "next"}); !strings.Contains(next, "Feature") {
		t.Fatalf("next = %q, want feature after prerequisite completion", next)
	}
}

func TestTasksToolUpdateGetAndDelete(t *testing.T) {
	tool, ctx, store, projectID := newTasksToolFixture(t)

	runTasksTool(t, tool, ctx, map[string]any{"action": "create", "title": "Refactor board"})
	runTasksTool(t, tool, ctx, map[string]any{"action": "update", "ref": "AG-1", "status": "in_review", "body": "PR #12"})

	output := runTasksTool(t, tool, ctx, map[string]any{"action": "get", "ref": "AG-1"})
	if !strings.Contains(output, "in_review") || !strings.Contains(output, "PR #12") {
		t.Fatalf("get output = %q", output)
	}

	runTasksTool(t, tool, ctx, map[string]any{"action": "delete", "ref": "AG-1"})
	tasks, err := store.ListTasks(projectID)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("tasks after delete = %#v, err = %v", tasks, err)
	}
}

func TestTasksToolRequiresProjectScopedSession(t *testing.T) {
	server, store := newProjectsAPITestServer(t)
	defer store.Close()

	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("Create session error = %v", err)
	}
	ctx := context.WithValue(context.Background(), sessionIDContextKey, sess.ID)
	raw, _ := json.Marshal(map[string]any{"action": "list"})
	result, err := newTasksTool(server).Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "project") {
		t.Fatalf("result = %#v, want project scope error", result)
	}
}
