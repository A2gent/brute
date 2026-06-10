package storage

import (
	"testing"
	"time"
)

func TestSQLiteStoreProjectTestCacheRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	project := &Project{
		ID:        "project-test-cache",
		Name:      "Test Cache",
		Settings:  map[string]string{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	cache := &ProjectTestCache{
		ProjectID:            project.ID,
		RepoPath:             "apps/api/",
		Branch:               "feature/tests",
		BaseBranch:           "main",
		ScopeHash:            "scope-1",
		TestResponseJSON:     `{"summary":{"total":1}}`,
		CoverageResponseJSON: `{"reports":[]}`,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := store.SaveProjectTestCache(cache); err != nil {
		t.Fatalf("SaveProjectTestCache: %v", err)
	}

	got, err := store.GetProjectTestCache(project.ID, "apps/api", "feature/tests", "main", "scope-1")
	if err != nil {
		t.Fatalf("GetProjectTestCache: %v", err)
	}
	if got == nil {
		t.Fatal("GetProjectTestCache returned nil")
	}
	if got.RepoPath != "apps/api" {
		t.Fatalf("repo path = %q, want apps/api", got.RepoPath)
	}
	if got.TestResponseJSON != cache.TestResponseJSON {
		t.Fatalf("test response = %q, want %q", got.TestResponseJSON, cache.TestResponseJSON)
	}

	cache.TestResponseJSON = `{"summary":{"total":2}}`
	cache.UpdatedAt = now.Add(time.Minute)
	if err := store.SaveProjectTestCache(cache); err != nil {
		t.Fatalf("SaveProjectTestCache update: %v", err)
	}

	got, err = store.GetProjectTestCache(project.ID, "apps/api", "feature/tests", "main", "scope-1")
	if err != nil {
		t.Fatalf("GetProjectTestCache after update: %v", err)
	}
	if got == nil || got.TestResponseJSON != cache.TestResponseJSON {
		t.Fatalf("updated cache = %#v, want response %q", got, cache.TestResponseJSON)
	}

	missing, err := store.GetProjectTestCache(project.ID, "apps/api", "feature/tests", "main", "scope-2")
	if err != nil {
		t.Fatalf("GetProjectTestCache missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("missing cache = %#v, want nil", missing)
	}
}
