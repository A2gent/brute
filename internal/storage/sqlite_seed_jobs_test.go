package storage

import (
	"strings"
	"testing"
)

func TestArchivedSessionsReviewPromptBoundsPlaybook(t *testing.T) {
	for _, required := range []string{"at most 30 active lessons", "remove stale/session-specific entries", "merge duplicates", "keep each lesson to one line"} {
		if !strings.Contains(archivedSessionsReviewPrompt, required) {
			t.Errorf("archived review prompt must include %q", required)
		}
	}
}

func TestBuiltInArchivedSessionsReviewJobIsSeededOnce(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	job, err := store.GetJob(BuiltInArchivedSessionsReviewJobID)
	if err != nil {
		t.Fatalf("expected seeded job: %v", err)
	}
	if !job.Enabled || job.ProjectID != nil || job.NextRunAt == nil || job.ScheduleCron != archivedSessionsReviewCron {
		t.Fatalf("unexpected seeded job: %+v", job)
	}
	if err := store.DeleteJob(job.ID); err != nil {
		t.Fatalf("delete job: %v", err)
	}
	store.Close()

	// Reopening runs migrations again; a deleted built-in job must stay deleted.
	reopened, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	if _, err := reopened.GetJob(BuiltInArchivedSessionsReviewJobID); err == nil {
		t.Fatalf("deleted built-in job was re-seeded")
	}
}

func TestBuiltInJobsSkippedAndRemovedForChildRuntimes(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if _, err := store.GetJob(BuiltInArchivedSessionsReviewJobID); err != nil {
		t.Fatalf("expected job seeded before child opt-out: %v", err)
	}
	store.Close()

	// Children started before the opt-out already carry an invisible copy.
	t.Setenv(ChildRuntimeEnv, "1")
	reopened, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	if _, err := reopened.GetJob(BuiltInArchivedSessionsReviewJobID); err == nil {
		t.Fatalf("child store kept built-in job")
	}

	fresh, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("create child store: %v", err)
	}
	defer fresh.Close()
	if _, err := fresh.GetJob(BuiltInArchivedSessionsReviewJobID); err == nil {
		t.Fatalf("child store seeded built-in job")
	}
}
