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
