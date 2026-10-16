package scheduler

import (
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestRescheduleNotifiesAfterPersistence(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Second)
	job := &storage.RecurringJob{ID: "loop", Name: "Loop", ScheduleCron: "* * * * *", CreatedAt: now, UpdatedAt: now}
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{store: store}
	calls := 0
	s.SetJobChangeListener(func(changed *storage.RecurringJob) {
		calls++
		persisted, err := store.GetJob(changed.ID)
		if err != nil || persisted.LastRunAt == nil || !persisted.LastRunAt.Equal(now) || persisted.NextRunAt == nil {
			t.Fatalf("callback preceded persistence: %+v, err=%v", persisted, err)
		}
		// The callback must run outside the scheduler mutex.
		s.SetJobChangeListener(nil)
	})
	s.rescheduleJobAfterAttempt(job, now)
	if calls != 1 {
		t.Fatalf("listener calls=%d, want 1", calls)
	}
	if err := store.DeleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	s.SetJobChangeListener(func(*storage.RecurringJob) { t.Fatal("deleted job notified") })
	s.rescheduleJobAfterAttempt(job, now)
	_ = store.Close()
	s.rescheduleJobAfterAttempt(job, now)
}
