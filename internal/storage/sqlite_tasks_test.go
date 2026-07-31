package storage

import (
	"strings"
	"testing"
	"time"
)

func TestSQLiteTasksAreProjectScopedAndAllocateRefsPerProject(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	projectA := saveTaskTestProject(t, store, "project-a", "Alpha Two Gent")
	projectB := saveTaskTestProject(t, store, "project-b", "Beta")

	first, err := store.CreateTask(projectA.ID, TaskCreate{Title: "First task", Status: TaskStatusTodo})
	if err != nil {
		t.Fatalf("CreateTask(project A) error = %v", err)
	}
	second, err := store.CreateTask(projectA.ID, TaskCreate{Title: "Second task", Status: TaskStatusInProgress})
	if err != nil {
		t.Fatalf("CreateTask(project A second) error = %v", err)
	}
	other, err := store.CreateTask(projectB.ID, TaskCreate{Title: "Other task", Status: TaskStatusTodo})
	if err != nil {
		t.Fatalf("CreateTask(project B) error = %v", err)
	}

	if first.Ref != "ATG-1" || second.Ref != "ATG-2" || other.Ref != "B-1" {
		t.Fatalf("refs = %q, %q, %q; want ATG-1, ATG-2, B-1", first.Ref, second.Ref, other.Ref)
	}

	listed, err := store.ListTasks(projectA.ID)
	if err != nil {
		t.Fatalf("ListTasks(project A) error = %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("ListTasks(project A) len = %d, want 2", len(listed))
	}
	for _, task := range listed {
		if task.ProjectID != projectA.ID {
			t.Fatalf("ListTasks(project A) leaked task from %q", task.ProjectID)
		}
	}
}

func TestSQLiteTasksRejectMissingProjectAndCrossProjectMutation(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	projectA := saveTaskTestProject(t, store, "project-a", "Alpha")
	projectB := saveTaskTestProject(t, store, "project-b", "Beta")
	task, err := store.CreateTask(projectA.ID, TaskCreate{Title: "Scoped task"})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	if _, err := store.CreateTask("", TaskCreate{Title: "Global task"}); err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("CreateTask(empty project) error = %v, want project validation", err)
	}
	newTitle := "Cross-project edit"
	if _, err := store.UpdateTask(projectB.ID, task.ID, TaskUpdate{Title: &newTitle}); err == nil {
		t.Fatal("UpdateTask from another project succeeded")
	}
	if err := store.DeleteTask(projectB.ID, task.ID); err == nil {
		t.Fatal("DeleteTask from another project succeeded")
	}
}

func saveTaskTestProject(t *testing.T, store *SQLiteStore, id, name string) *Project {
	t.Helper()
	now := time.Now().UTC()
	project := &Project{ID: id, Name: name, Settings: map[string]string{}, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject(%q) error = %v", id, err)
	}
	return project
}
