package storage

import (
	"testing"
	"time"
)

func TestSQLiteStoreProjectPRDescriptionRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	project := &Project{
		ID:        "project-pr-description",
		Name:      "PR Description",
		Settings:  map[string]string{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	description := &ProjectPRDescription{
		ProjectID:  project.ID,
		RepoPath:   "apps/caesar/",
		Branch:     "feature/pr-description",
		BaseBranch: "main",
		Content:    "## Why\nInitial\n\n## Changes\n- Add tab\n\n## Testing\nNot run",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := store.SaveProjectPRDescription(description); err != nil {
		t.Fatalf("SaveProjectPRDescription: %v", err)
	}

	got, err := store.GetProjectPRDescription(project.ID, "apps/caesar", "feature/pr-description", "main")
	if err != nil {
		t.Fatalf("GetProjectPRDescription: %v", err)
	}
	if got == nil {
		t.Fatal("GetProjectPRDescription returned nil")
	}
	if got.Content != description.Content {
		t.Fatalf("content = %q, want %q", got.Content, description.Content)
	}
	if got.RepoPath != "apps/caesar" {
		t.Fatalf("repo path = %q, want apps/caesar", got.RepoPath)
	}

	description.Content = "## Why\nUpdated\n\n## Changes\n- Keep edits\n\n## Testing\nNot run"
	description.UpdatedAt = now.Add(time.Minute)
	if err := store.SaveProjectPRDescription(description); err != nil {
		t.Fatalf("SaveProjectPRDescription update: %v", err)
	}

	got, err = store.GetProjectPRDescription(project.ID, "apps/caesar", "feature/pr-description", "main")
	if err != nil {
		t.Fatalf("GetProjectPRDescription after update: %v", err)
	}
	if got == nil || got.Content != description.Content {
		t.Fatalf("updated content = %#v, want %q", got, description.Content)
	}

	missing, err := store.GetProjectPRDescription(project.ID, "apps/caesar", "feature/other", "main")
	if err != nil {
		t.Fatalf("GetProjectPRDescription missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("missing description = %#v, want nil", missing)
	}
}
