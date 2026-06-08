package storage

import (
	"database/sql"
	"fmt"
	"time"
)

// --- Recurring Jobs CRUD ---

// SaveJob saves a recurring job to the database
func (s *SQLiteStore) SaveJob(job *RecurringJob) error {
	_, err := s.db.Exec(`
		INSERT INTO recurring_jobs (id, project_id, name, schedule_human, schedule_cron, task_prompt, task_prompt_source, task_prompt_file, llm_provider, enabled, last_run_at, next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			project_id = excluded.project_id,
			name = excluded.name,
			schedule_human = excluded.schedule_human,
			schedule_cron = excluded.schedule_cron,
			task_prompt = excluded.task_prompt,
			task_prompt_source = excluded.task_prompt_source,
			task_prompt_file = excluded.task_prompt_file,
			llm_provider = excluded.llm_provider,
			enabled = excluded.enabled,
			last_run_at = excluded.last_run_at,
			next_run_at = excluded.next_run_at,
			updated_at = excluded.updated_at
	`, job.ID, nullableString(job.ProjectID), job.Name, job.ScheduleHuman, job.ScheduleCron, job.TaskPrompt, job.TaskPromptSource, job.TaskPromptFile, job.LLMProvider, job.Enabled, job.LastRunAt, job.NextRunAt, job.CreatedAt, job.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save job: %w", err)
	}
	return nil
}

// GetJob retrieves a recurring job by ID
func (s *SQLiteStore) GetJob(id string) (*RecurringJob, error) {
	var job RecurringJob
	var projectID sql.NullString
	var lastRunAt, nextRunAt sql.NullTime
	var enabled int

	err := s.db.QueryRow(`
		SELECT id, project_id, name, schedule_human, schedule_cron, task_prompt, task_prompt_source, task_prompt_file, llm_provider, enabled, last_run_at, next_run_at, created_at, updated_at
		FROM recurring_jobs WHERE id = ?
	`, id).Scan(&job.ID, &projectID, &job.Name, &job.ScheduleHuman, &job.ScheduleCron, &job.TaskPrompt, &job.TaskPromptSource, &job.TaskPromptFile, &job.LLMProvider, &enabled, &lastRunAt, &nextRunAt, &job.CreatedAt, &job.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("job not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	job.Enabled = enabled == 1
	setNullableString(&job.ProjectID, projectID)
	if lastRunAt.Valid {
		job.LastRunAt = &lastRunAt.Time
	}
	if nextRunAt.Valid {
		job.NextRunAt = &nextRunAt.Time
	}

	return &job, nil
}

// ListJobs lists all recurring jobs
func (s *SQLiteStore) ListJobs() ([]*RecurringJob, error) {
	rows, err := s.db.Query(`
		SELECT id, project_id, name, schedule_human, schedule_cron, task_prompt, task_prompt_source, task_prompt_file, llm_provider, enabled, last_run_at, next_run_at, created_at, updated_at
		FROM recurring_jobs ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*RecurringJob
	for rows.Next() {
		var job RecurringJob
		var projectID sql.NullString
		var lastRunAt, nextRunAt sql.NullTime
		var enabled int

		err := rows.Scan(&job.ID, &projectID, &job.Name, &job.ScheduleHuman, &job.ScheduleCron, &job.TaskPrompt, &job.TaskPromptSource, &job.TaskPromptFile, &job.LLMProvider, &enabled, &lastRunAt, &nextRunAt, &job.CreatedAt, &job.UpdatedAt)
		if err != nil {
			return nil, err
		}

		job.Enabled = enabled == 1
		setNullableString(&job.ProjectID, projectID)
		if lastRunAt.Valid {
			job.LastRunAt = &lastRunAt.Time
		}
		if nextRunAt.Valid {
			job.NextRunAt = &nextRunAt.Time
		}

		jobs = append(jobs, &job)
	}

	return jobs, nil
}

// DeleteJob deletes a recurring job
func (s *SQLiteStore) DeleteJob(id string) error {
	_, err := s.db.Exec("DELETE FROM recurring_jobs WHERE id = ?", id)
	return err
}

// GetDueJobs returns jobs that are due to run (next_run_at <= now and enabled)
func (s *SQLiteStore) GetDueJobs(now time.Time) ([]*RecurringJob, error) {
	rows, err := s.db.Query(`
		SELECT id, project_id, name, schedule_human, schedule_cron, task_prompt, task_prompt_source, task_prompt_file, llm_provider, enabled, last_run_at, next_run_at, created_at, updated_at
		FROM recurring_jobs 
		WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?
		ORDER BY next_run_at ASC
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*RecurringJob
	for rows.Next() {
		var job RecurringJob
		var projectID sql.NullString
		var lastRunAt, nextRunAt sql.NullTime
		var enabled int

		err := rows.Scan(&job.ID, &projectID, &job.Name, &job.ScheduleHuman, &job.ScheduleCron, &job.TaskPrompt, &job.TaskPromptSource, &job.TaskPromptFile, &job.LLMProvider, &enabled, &lastRunAt, &nextRunAt, &job.CreatedAt, &job.UpdatedAt)
		if err != nil {
			return nil, err
		}

		job.Enabled = enabled == 1
		setNullableString(&job.ProjectID, projectID)
		if lastRunAt.Valid {
			job.LastRunAt = &lastRunAt.Time
		}
		if nextRunAt.Valid {
			job.NextRunAt = &nextRunAt.Time
		}

		jobs = append(jobs, &job)
	}

	return jobs, nil
}

// --- Job Executions CRUD ---

// SaveJobExecution saves a job execution to the database
func (s *SQLiteStore) SaveJobExecution(exec *JobExecution) error {
	_, err := s.db.Exec(`
		INSERT INTO job_executions (id, job_id, session_id, status, output, error, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			job_id = excluded.job_id,
			session_id = excluded.session_id,
			status = excluded.status,
			output = excluded.output,
			error = excluded.error,
			started_at = excluded.started_at,
			finished_at = excluded.finished_at
	`, exec.ID, exec.JobID, exec.SessionID, exec.Status, exec.Output, exec.Error, exec.StartedAt, exec.FinishedAt)
	if err != nil {
		return fmt.Errorf("failed to save job execution: %w", err)
	}
	return nil
}

// GetJobExecution retrieves a job execution by ID
func (s *SQLiteStore) GetJobExecution(id string) (*JobExecution, error) {
	var exec JobExecution
	var sessionID sql.NullString
	var finishedAt sql.NullTime
	var output, execError sql.NullString

	err := s.db.QueryRow(`
		SELECT id, job_id, session_id, status, output, error, started_at, finished_at
		FROM job_executions WHERE id = ?
	`, id).Scan(&exec.ID, &exec.JobID, &sessionID, &exec.Status, &output, &execError, &exec.StartedAt, &finishedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("job execution not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	if sessionID.Valid {
		exec.SessionID = sessionID.String
	}
	if output.Valid {
		exec.Output = output.String
	}
	if execError.Valid {
		exec.Error = execError.String
	}
	if finishedAt.Valid {
		exec.FinishedAt = &finishedAt.Time
	}

	return &exec, nil
}

// ListJobExecutions lists executions for a job, ordered by most recent first
func (s *SQLiteStore) ListJobExecutions(jobID string, limit int) ([]*JobExecution, error) {
	rows, err := s.db.Query(`
		SELECT id, job_id, session_id, status, output, error, started_at, finished_at
		FROM job_executions 
		WHERE job_id = ?
		ORDER BY started_at DESC
		LIMIT ?
	`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []*JobExecution
	for rows.Next() {
		var exec JobExecution
		var sessionID sql.NullString
		var finishedAt sql.NullTime
		var output, execError sql.NullString

		err := rows.Scan(&exec.ID, &exec.JobID, &sessionID, &exec.Status, &output, &execError, &exec.StartedAt, &finishedAt)
		if err != nil {
			return nil, err
		}

		if sessionID.Valid {
			exec.SessionID = sessionID.String
		}
		if output.Valid {
			exec.Output = output.String
		}
		if execError.Valid {
			exec.Error = execError.String
		}
		if finishedAt.Valid {
			exec.FinishedAt = &finishedAt.Time
		}

		executions = append(executions, &exec)
	}

	return executions, nil
}
