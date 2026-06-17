package storage

import (
	"testing"
	"time"
)

func TestSQLiteStoreProjectGitReviewOverlayCacheRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	project := &Project{
		ID:        "project-review-overlay-cache",
		Name:      "Review Overlay Cache",
		Settings:  map[string]string{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	cache := &ProjectGitReviewOverlayCache{
		ProjectID:       project.ID,
		RepoPath:        "apps/caesar/",
		Branch:          "feature/review-overlay",
		BaseBranch:      "main",
		FilePath:        "a/src/app.ts",
		DiffHash:        "hash-1",
		AnnotationsJSON: `[{"file_path":"src/app.ts","side":"additions","line_number":12,"title":"Validation prevents empty saves","body":"The save flow now stops invalid requests before they reach persistence, so users get a clear validation path instead of a backend error."}]`,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := store.SaveProjectGitReviewOverlayCache(cache); err != nil {
		t.Fatalf("SaveProjectGitReviewOverlayCache: %v", err)
	}

	items, err := store.ListProjectGitReviewOverlayCache(project.ID, "apps/caesar", "feature/review-overlay", "main")
	if err != nil {
		t.Fatalf("ListProjectGitReviewOverlayCache: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("cache rows = %d, want 1", len(items))
	}
	if items[0].RepoPath != "apps/caesar" || items[0].FilePath != "src/app.ts" || items[0].DiffHash != "hash-1" {
		t.Fatalf("unexpected cache row: %#v", items[0])
	}

	cache.DiffHash = "hash-2"
	cache.AnnotationsJSON = `[]`
	cache.UpdatedAt = now.Add(time.Minute)
	if err := store.SaveProjectGitReviewOverlayCache(cache); err != nil {
		t.Fatalf("SaveProjectGitReviewOverlayCache update: %v", err)
	}

	items, err = store.ListProjectGitReviewOverlayCache(project.ID, "apps/caesar", "feature/review-overlay", "main")
	if err != nil {
		t.Fatalf("ListProjectGitReviewOverlayCache after update: %v", err)
	}
	if len(items) != 1 || items[0].DiffHash != "hash-2" || items[0].AnnotationsJSON != `[]` {
		t.Fatalf("updated cache = %#v", items)
	}

	missing, err := store.ListProjectGitReviewOverlayCache(project.ID, "apps/caesar", "feature/other", "main")
	if err != nil {
		t.Fatalf("ListProjectGitReviewOverlayCache missing: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing cache rows = %#v, want empty", missing)
	}
}
