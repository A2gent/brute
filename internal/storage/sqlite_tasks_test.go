package storage

import (
	"strconv"
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

func TestSQLiteTaskPersistsImageAndLinkedSession(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := saveTaskTestProject(t, store, "project", "Project")
	task, err := store.CreateTask(project.ID, TaskCreate{Title: "Visual task", Image: &TaskImage{Name: "shot.png", MediaType: "image/png", DataBase64: "aGVsbG8="}})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "session-1"
	if _, err = store.UpdateTask(project.ID, task.ID, TaskUpdate{SessionID: &sessionID}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetTask(project.ID, task.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Image == nil || loaded.Image.Name != "shot.png" || loaded.SessionID != sessionID {
		t.Fatalf("loaded task = %#v", loaded)
	}
}

func TestSQLiteTaskDependenciesPersistAndRejectCycles(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := saveTaskTestProject(t, store, "project", "Project")
	foundation, err := store.CreateTask(project.ID, TaskCreate{Title: "Foundation"})
	if err != nil {
		t.Fatal(err)
	}
	feature, err := store.CreateTask(project.ID, TaskCreate{Title: "Feature", DependencyRefs: []string{foundation.Ref}})
	if err != nil {
		t.Fatal(err)
	}
	if len(feature.DependencyIDs) != 1 || feature.DependencyIDs[0] != foundation.ID {
		t.Fatalf("feature dependencies = %#v, want %q", feature.DependencyIDs, foundation.ID)
	}
	loaded, err := store.GetTask(project.ID, feature.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.DependencyIDs) != 1 || loaded.DependencyIDs[0] != foundation.ID {
		t.Fatalf("loaded dependencies = %#v", loaded.DependencyIDs)
	}
	if _, err := store.UpdateTask(project.ID, foundation.Ref, TaskUpdate{DependencyRefs: &[]string{feature.Ref}}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle update error = %v, want cycle validation", err)
	}
	if _, err := store.UpdateTask(project.ID, feature.Ref, TaskUpdate{DependencyRefs: &[]string{feature.Ref}}); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("self dependency error = %v, want self validation", err)
	}
}

func TestSQLiteTaskRefPrefixSkipsEmojiAndNonASCIILetters(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	project := saveTaskTestProject(t, store, "emoji-project", "Агент 🤖 Alpha")
	task, err := store.CreateTask(project.ID, TaskCreate{Title: "Task"})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if task.Ref != "A-1" {
		t.Fatalf("task ref = %q, want A-1", task.Ref)
	}
	if _, err := store.GetTask(project.ID, task.Ref); err != nil {
		t.Fatalf("GetTask(%q) error = %v", task.Ref, err)
	}
}

func TestSQLiteTaskRefPrefixFallsBackWhenProjectNameHasNoASCIIInitials(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	project := saveTaskTestProject(t, store, "non-ascii-project", "Агент 🤖")
	task, err := store.CreateTask(project.ID, TaskCreate{Title: "Task"})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if task.Ref != "T-1" {
		t.Fatalf("task ref = %q, want T-1", task.Ref)
	}
	if _, err := store.GetTask(project.ID, task.Ref); err != nil {
		t.Fatalf("GetTask(%q) error = %v", task.Ref, err)
	}
}
func TestMigrateBrokenTaskRefsPreservesCustomRefs(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	project := saveTaskTestProject(t, store, "custom-refs", "Alpha")
	task, err := store.CreateTask(project.ID, TaskCreate{Title: "Custom"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE tasks SET ref = ? WHERE id = ?`, "custom-"+strconv.Itoa(task.Seq), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateBrokenTaskRefs(); err != nil {
		t.Fatalf("migrateBrokenTaskRefs() error = %v", err)
	}
	if _, err := store.GetTask(project.ID, "custom-"+strconv.Itoa(task.Seq)); err != nil {
		t.Fatalf("custom ref not preserved: %v", err)
	}
}

func TestMigrateBrokenTaskRefs(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	project := saveTaskTestProject(t, store, "broken-refs", "Агент 🤖")

	for seq := 88; seq <= 92; seq++ {
		task, err := store.CreateTask(project.ID, TaskCreate{Title: "Task"})
		if err != nil {
			t.Fatalf("CreateTask() error = %v", err)
		}
		if _, err := store.db.Exec(`UPDATE tasks SET seq = ?, ref = ? WHERE id = ?`, seq, "��-"+strconv.Itoa(seq), task.ID); err != nil {
			t.Fatalf("inject broken ref: %v", err)
		}
	}

	if err := store.migrateBrokenTaskRefs(); err != nil {
		t.Fatalf("migrateBrokenTaskRefs() error = %v", err)
	}
	for seq := 88; seq <= 92; seq++ {
		ref := "T-" + strconv.Itoa(seq)
		if _, err := store.GetTask(project.ID, ref); err != nil {
			t.Errorf("GetTask(%q) error = %v", ref, err)
		}
	}
}

// Production rows hold raw invalid UTF-8 bytes (not literal U+FFFD runes).
func TestMigrateBrokenTaskRefsRawInvalidBytes(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	project := saveTaskTestProject(t, store, "raw-broken", "Агент 🤖")
	task, err := store.CreateTask(project.ID, TaskCreate{Title: "Task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE tasks SET seq = 93, ref = CAST(x'D0F02D3933' AS TEXT) WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateBrokenTaskRefs(); err != nil {
		t.Fatalf("migrateBrokenTaskRefs() error = %v", err)
	}
	if _, err := store.GetTask(project.ID, "T-93"); err != nil {
		t.Fatalf("GetTask(T-93) error = %v", err)
	}
}

func TestSQLiteTaskPersistsHours(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := saveTaskTestProject(t, store, "project", "Project")
	task, err := store.CreateTask(project.ID, TaskCreate{Title: "Timed task", Hours: 23})
	if err != nil {
		t.Fatal(err)
	}
	if task.Hours != 23 {
		t.Fatalf("created hours = %d, want 23", task.Hours)
	}
	loaded, err := store.GetTask(project.ID, task.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Hours != 23 {
		t.Fatalf("loaded hours = %d, want 23", loaded.Hours)
	}
	hours := 1
	updated, err := store.UpdateTask(project.ID, task.Ref, TaskUpdate{Hours: &hours})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Hours != 1 {
		t.Fatalf("updated hours = %d, want 1", updated.Hours)
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
