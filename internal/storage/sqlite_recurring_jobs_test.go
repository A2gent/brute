package storage

import (
	"testing"
	"time"
)

func TestProjectRecurringJobPersistenceAndDelete(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	projectID := "project-recurring-job-test"
	projectFolder := t.TempDir()
	project := &Project{
		ID:        projectID,
		Name:      "Recurring Job Test",
		Folder:    &projectFolder,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	nextRun := now.Add(-time.Minute)
	job := &RecurringJob{
		ID:               "job-project-scoped",
		ProjectID:        &projectID,
		Name:             "Project scoped job",
		ScheduleHuman:    "every hour",
		ScheduleCron:     "0 * * * *",
		TaskPrompt:       "Do the project thing",
		TaskPromptSource: "text",
		Enabled:          true,
		NextRunAt:        &nextRun,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	got, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.ProjectID == nil || *got.ProjectID != projectID {
		t.Fatalf("GetJob ProjectID = %v, want %q", got.ProjectID, projectID)
	}

	due, err := store.GetDueJobs(now)
	if err != nil {
		t.Fatalf("GetDueJobs: %v", err)
	}
	if len(due) != 1 || due[0].ProjectID == nil || *due[0].ProjectID != projectID {
		t.Fatalf("GetDueJobs project IDs = %#v, want one job in %q", due, projectID)
	}

	if err := store.SaveJobExecution(&JobExecution{
		ID:        "exec-project-scoped",
		JobID:     job.ID,
		Status:    "success",
		StartedAt: now,
	}); err != nil {
		t.Fatalf("SaveJobExecution: %v", err)
	}

	if err := store.DeleteProject(projectID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := store.GetJob(job.ID); err == nil {
		t.Fatal("GetJob after DeleteProject succeeded, want not found")
	}
	executions, err := store.ListJobExecutions(job.ID, 10)
	if err != nil {
		t.Fatalf("ListJobExecutions after DeleteProject: %v", err)
	}
	if len(executions) != 0 {
		t.Fatalf("ListJobExecutions after DeleteProject returned %d executions, want 0", len(executions))
	}
}

func TestListSessionsIncludesRecurringJobSessions(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	jobID := "job-visible-in-session-list"
	regularSession := &Session{
		ID:        "regular-session",
		AgentID:   "build",
		Title:     "Regular session",
		Status:    "completed",
		Metadata:  map[string]interface{}{},
		CreatedAt: now.Add(-time.Minute),
		UpdatedAt: now.Add(-time.Minute),
	}
	jobSession := &Session{
		ID:        "job-session",
		AgentID:   "build",
		JobID:     &jobID,
		Title:     "Scheduled session",
		Status:    "completed",
		Metadata:  map[string]interface{}{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveSession(regularSession); err != nil {
		t.Fatalf("SaveSession regular: %v", err)
	}
	if err := store.SaveSession(jobSession); err != nil {
		t.Fatalf("SaveSession job: %v", err)
	}

	sessions, err := store.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("ListSessions returned %d sessions, want 2", len(sessions))
	}

	byID := map[string]*Session{}
	for _, sess := range sessions {
		byID[sess.ID] = sess
	}
	if byID[regularSession.ID] == nil {
		t.Fatalf("ListSessions missing regular session %q", regularSession.ID)
	}
	listedJobSession := byID[jobSession.ID]
	if listedJobSession == nil {
		t.Fatalf("ListSessions missing recurring job session %q", jobSession.ID)
	}
	if listedJobSession.JobID == nil || *listedJobSession.JobID != jobID {
		t.Fatalf("ListSessions job_id = %v, want %q", listedJobSession.JobID, jobID)
	}
}
