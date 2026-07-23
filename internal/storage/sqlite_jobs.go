package storage

import (
	"database/sql"
	"fmt"
	"time"
)

// --- Recurring Jobs CRUD ---

const recurringJobSelectColumns = `
	id, project_id, name, schedule_human, schedule_cron, task_prompt, task_prompt_source, task_prompt_file,
	run_target, workflow_id, workflow_name, workflow_definition,
	launch_agent_id, launch_agent_name, launch_agent_runtime, unified_agent_id, docker_agent_id,
	llm_provider, llm_model, enabled, last_run_at, next_run_at, created_at, updated_at
`

func scanRecurringJob(
	scan func(dest ...interface{}) error,
) (*RecurringJob, error) {
	var job RecurringJob
	var projectID sql.NullString
	var lastRunAt, nextRunAt sql.NullTime
	var enabled int

	err := scan(
		&job.ID, &projectID, &job.Name, &job.ScheduleHuman, &job.ScheduleCron, &job.TaskPrompt, &job.TaskPromptSource, &job.TaskPromptFile,
		&job.RunTarget, &job.WorkflowID, &job.WorkflowName, &job.WorkflowDefJSON,
		&job.LaunchAgentID, &job.LaunchAgentName, &job.LaunchAgentRun, &job.UnifiedAgentID, &job.DockerAgentID,
		&job.LLMProvider, &job.LLMModel, &enabled, &lastRunAt, &nextRunAt, &job.CreatedAt, &job.UpdatedAt,
	)
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

// SaveJob saves a recurring job to the database
func (s *SQLiteStore) SaveJob(job *RecurringJob) error {
	_, err := s.db.Exec(`
		INSERT INTO recurring_jobs (`+recurringJobSelectColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			project_id = excluded.project_id,
			name = excluded.name,
			schedule_human = excluded.schedule_human,
			schedule_cron = excluded.schedule_cron,
			task_prompt = excluded.task_prompt,
			task_prompt_source = excluded.task_prompt_source,
			task_prompt_file = excluded.task_prompt_file,
			run_target = excluded.run_target,
			workflow_id = excluded.workflow_id,
			workflow_name = excluded.workflow_name,
			workflow_definition = excluded.workflow_definition,
			launch_agent_id = excluded.launch_agent_id,
			launch_agent_name = excluded.launch_agent_name,
			launch_agent_runtime = excluded.launch_agent_runtime,
			unified_agent_id = excluded.unified_agent_id,
			docker_agent_id = excluded.docker_agent_id,
			llm_provider = excluded.llm_provider,
			llm_model = excluded.llm_model,
			enabled = excluded.enabled,
			last_run_at = excluded.last_run_at,
			next_run_at = excluded.next_run_at,
			updated_at = excluded.updated_at
	`, job.ID, nullableString(job.ProjectID), job.Name, job.ScheduleHuman, job.ScheduleCron, job.TaskPrompt, job.TaskPromptSource, job.TaskPromptFile,
		job.RunTarget, job.WorkflowID, job.WorkflowName, job.WorkflowDefJSON,
		job.LaunchAgentID, job.LaunchAgentName, job.LaunchAgentRun, job.UnifiedAgentID, job.DockerAgentID,
		job.LLMProvider, job.LLMModel, job.Enabled, job.LastRunAt, job.NextRunAt, job.CreatedAt, job.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save job: %w", err)
	}
	return nil
}

// UpdateExistingJob updates schedule/run fields only when the job row still exists.
// WHY: SaveJob uses INSERT and would resurrect a loop deleted while a run was in flight.
func (s *SQLiteStore) UpdateExistingJob(job *RecurringJob) (bool, error) {
	result, err := s.db.Exec(`
		UPDATE recurring_jobs SET
			project_id = ?,
			name = ?,
			schedule_human = ?,
			schedule_cron = ?,
			task_prompt = ?,
			task_prompt_source = ?,
			task_prompt_file = ?,
			run_target = ?,
			workflow_id = ?,
			workflow_name = ?,
			workflow_definition = ?,
			launch_agent_id = ?,
			launch_agent_name = ?,
			launch_agent_runtime = ?,
			unified_agent_id = ?,
			docker_agent_id = ?,
			llm_provider = ?,
			llm_model = ?,
			enabled = ?,
			last_run_at = ?,
			next_run_at = ?,
			updated_at = ?
		WHERE id = ?
	`, nullableString(job.ProjectID), job.Name, job.ScheduleHuman, job.ScheduleCron, job.TaskPrompt, job.TaskPromptSource, job.TaskPromptFile,
		job.RunTarget, job.WorkflowID, job.WorkflowName, job.WorkflowDefJSON,
		job.LaunchAgentID, job.LaunchAgentName, job.LaunchAgentRun, job.UnifiedAgentID, job.DockerAgentID,
		job.LLMProvider, job.LLMModel, job.Enabled, job.LastRunAt, job.NextRunAt, job.UpdatedAt, job.ID)
	if err != nil {
		return false, fmt.Errorf("failed to update job: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to read job update rows: %w", err)
	}
	return rows > 0, nil
}

// GetJob retrieves a recurring job by ID
func (s *SQLiteStore) GetJob(id string) (*RecurringJob, error) {
	row := s.db.QueryRow(`
		SELECT `+recurringJobSelectColumns+`
		FROM recurring_jobs WHERE id = ?
	`, id)
	job, err := scanRecurringJob(row.Scan)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("job not found: %s", id)
	}
	if err != nil {
		return nil, err
	}
	return job, nil
}

// ListJobs lists all recurring jobs
func (s *SQLiteStore) ListJobs() ([]*RecurringJob, error) {
	rows, err := s.db.Query(`
		SELECT ` + recurringJobSelectColumns + `
		FROM recurring_jobs ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*RecurringJob
	for rows.Next() {
		job, err := scanRecurringJob(rows.Scan)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
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
		SELECT `+recurringJobSelectColumns+`
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
		job, err := scanRecurringJob(rows.Scan)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
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
