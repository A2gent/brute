package http

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestImportNextMarkdownTaskRemovesOnlyImportedEntry(t *testing.T) {
	server, store := newProjectsAPITestServer(t)
	defer store.Close()

	root := t.TempDir()
	project := saveImportTestProject(t, store, root)
	todoPath := filepath.Join(root, "TODO.md")
	original := "# TODO\n\n- [ ] First task #ui 120 EUR\n- [ ] Second task\n"
	if err := os.WriteFile(todoPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write TODO.md: %v", err)
	}

	result, err := server.importNextMarkdownTask(project.ID, "TODO.md")
	if err != nil {
		t.Fatalf("importNextMarkdownTask() error = %v", err)
	}
	if result.Imported == nil || result.Imported.Title != "First task #ui 120 EUR" {
		t.Fatalf("imported = %#v", result.Imported)
	}
	if result.Remaining != 1 || result.SourceDeleted {
		t.Fatalf("result = %#v, want one remaining and source kept", result)
	}

	content, err := os.ReadFile(todoPath)
	if err != nil {
		t.Fatalf("read TODO.md: %v", err)
	}
	if strings.Contains(string(content), "First task") || !strings.Contains(string(content), "Second task") {
		t.Fatalf("TODO.md after first import = %q", content)
	}

	tasks, err := store.ListTasks(project.ID)
	if err != nil {
		t.Fatalf("ListTasks() error = %v", err)
	}
	if len(tasks) != 1 || tasks[0].ProjectID != project.ID {
		t.Fatalf("tasks = %#v", tasks)
	}

	result, err = server.importNextMarkdownTask(project.ID, "TODO.md")
	if err != nil {
		t.Fatalf("second importNextMarkdownTask() error = %v", err)
	}
	if result.Remaining != 0 || !result.SourceDeleted {
		t.Fatalf("second result = %#v, want deleted source", result)
	}
	if _, err := os.Stat(todoPath); !os.IsNotExist(err) {
		t.Fatalf("TODO.md still exists: %v", err)
	}
}

func TestImportNextMarkdownTaskKeepsLineWhenCleanupFails(t *testing.T) {
	server, store := newProjectsAPITestServer(t)
	defer store.Close()

	root := t.TempDir()
	project := saveImportTestProject(t, store, root)
	todoPath := filepath.Join(root, "TODO.md")
	line := "- [ ] Keep me until cleanup succeeds\n"
	if err := os.WriteFile(todoPath, []byte(line), 0o444); err != nil {
		t.Fatalf("write TODO.md: %v", err)
	}

	server.removeTaskSource = func(string) error { return os.ErrPermission }
	if _, err := server.importNextMarkdownTask(project.ID, "TODO.md"); err == nil {
		t.Fatal("importNextMarkdownTask() succeeded despite cleanup failure")
	}
	content, err := os.ReadFile(todoPath)
	if err != nil {
		t.Fatalf("read TODO.md: %v", err)
	}
	if string(content) != line {
		t.Fatalf("TODO.md changed after cleanup failure: %q", content)
	}

	// The persisted row makes the retry idempotent; only cleanup remains.
	tasks, err := store.ListTasks(project.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks after cleanup failure = %#v, %v", tasks, err)
	}
	server.removeTaskSource = os.Remove
	result, err := server.importNextMarkdownTask(project.ID, "TODO.md")
	if err != nil {
		t.Fatalf("retry importNextMarkdownTask() error = %v", err)
	}
	if !result.SourceDeleted || result.Remaining != 0 {
		t.Fatalf("retry result = %#v", result)
	}
	tasks, err = store.ListTasks(project.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("retry duplicated tasks: %#v, %v", tasks, err)
	}
}

func saveImportTestProject(t *testing.T, store *storage.SQLiteStore, root string) *storage.Project {
	t.Helper()
	folder := root
	project := &storage.Project{ID: "project-import", Name: "Import Project", Folder: &folder}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject() error = %v", err)
	}
	return project
}
