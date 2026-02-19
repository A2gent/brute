package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore implements Store using SQLite
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates a new SQLite store
func NewSQLiteStore(dataPath string) (*SQLiteStore, error) {
	dbPath := filepath.Join(dataPath, "aagent.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	// SQLite supports one writer at a time. Keep pool size constrained to
	// reduce lock contention ("SQLITE_BUSY") under concurrent goroutines.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	store := &SQLiteStore{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return store, nil
}

// migrate runs database migrations
func (s *SQLiteStore) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			parent_id TEXT,
			project_id TEXT,
			title TEXT DEFAULT '',
			status TEXT NOT NULL,
			metadata TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT,
			tool_calls TEXT,
			tool_results TEXT,
			metadata TEXT,
			timestamp TIMESTAMP NOT NULL,
			FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session_id ON messages(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_parent_id ON sessions(parent_id)`,
		// Migration to add title column if it doesn't exist
		`ALTER TABLE sessions ADD COLUMN title TEXT DEFAULT ''`,
		// Migration to add project_id column to sessions
		`ALTER TABLE sessions ADD COLUMN project_id TEXT`,
		// Migration to add metadata column to messages
		`ALTER TABLE messages ADD COLUMN metadata TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_project_id ON sessions(project_id)`,
		// Recurring jobs table
		`CREATE TABLE IF NOT EXISTS recurring_jobs (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			schedule_human TEXT NOT NULL,
			schedule_cron TEXT NOT NULL,
			task_prompt TEXT NOT NULL,
			task_prompt_source TEXT NOT NULL DEFAULT 'text',
			task_prompt_file TEXT NOT NULL DEFAULT '',
			llm_provider TEXT,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_run_at TIMESTAMP,
			next_run_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`ALTER TABLE recurring_jobs ADD COLUMN task_prompt_source TEXT NOT NULL DEFAULT 'text'`,
		`ALTER TABLE recurring_jobs ADD COLUMN task_prompt_file TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE recurring_jobs ADD COLUMN llm_provider TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_recurring_jobs_next_run ON recurring_jobs(next_run_at)`,
		`CREATE INDEX IF NOT EXISTS idx_recurring_jobs_enabled ON recurring_jobs(enabled)`,
		// Job executions table
		`CREATE TABLE IF NOT EXISTS job_executions (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			session_id TEXT,
			status TEXT NOT NULL,
			output TEXT,
			error TEXT,
			started_at TIMESTAMP NOT NULL,
			finished_at TIMESTAMP,
			FOREIGN KEY (job_id) REFERENCES recurring_jobs(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_job_executions_job_id ON job_executions(job_id)`,
		`CREATE INDEX IF NOT EXISTS idx_job_executions_started_at ON job_executions(started_at)`,
		// Migration: Add job_id column to sessions
		`ALTER TABLE sessions ADD COLUMN job_id TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_job_id ON sessions(job_id)`,
		// App settings key/value table (secrets/tokens and other runtime settings)
		`CREATE TABLE IF NOT EXISTS app_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		// Channel integrations (Telegram/Slack/Discord/WhatsApp/Webhook)
		`CREATE TABLE IF NOT EXISTS integrations (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			name TEXT NOT NULL,
			mode TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			config TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_integrations_provider ON integrations(provider)`,
		// MCP server registry
		`CREATE TABLE IF NOT EXISTS mcp_servers (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			transport TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			config TEXT NOT NULL,
			last_test_at TIMESTAMP,
			last_test_success INTEGER,
			last_test_message TEXT,
			last_estimated_tokens INTEGER,
			last_tool_count INTEGER,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`ALTER TABLE mcp_servers ADD COLUMN last_test_at TIMESTAMP`,
		`ALTER TABLE mcp_servers ADD COLUMN last_test_success INTEGER`,
		`ALTER TABLE mcp_servers ADD COLUMN last_test_message TEXT`,
		`ALTER TABLE mcp_servers ADD COLUMN last_estimated_tokens INTEGER`,
		`ALTER TABLE mcp_servers ADD COLUMN last_tool_count INTEGER`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_servers_transport ON mcp_servers(transport)`,
		// Projects for optional session grouping
		`CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			folders TEXT NOT NULL DEFAULT '[]',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_name ON projects(name)`,
		// Migration: Add is_system column to projects
		`ALTER TABLE projects ADD COLUMN is_system INTEGER NOT NULL DEFAULT 0`,
		// Migration: Change folders to folder (single folder, nullable)
		`ALTER TABLE projects ADD COLUMN folder TEXT`,
		// Migration: Add task_progress column to sessions
		`ALTER TABLE sessions ADD COLUMN task_progress TEXT`,
	}

	for _, m := range migrations {
		// Ignore errors for ALTER TABLE (column may already exist)
		_, err := s.db.Exec(m)
		if err != nil && m[:5] != "ALTER" {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	// Seed system projects (idempotent - uses INSERT OR IGNORE)
	if err := s.seedSystemProjects(); err != nil {
		return fmt.Errorf("failed to seed system projects: %w", err)
	}

	return nil
}

// System project IDs - must match frontend constants in Sidebar.tsx
const (
	SystemProjectKBID    = "system-kb"
	SystemProjectAgentID = "system-agent"
)

// seedSystemProjects creates the system projects if they don't exist.
// These are required for the Knowledge Base and Agent session lists in the sidebar.
func (s *SQLiteStore) seedSystemProjects() error {
	systemProjects := []struct {
		id   string
		name string
	}{
		{SystemProjectKBID, "Knowledge Base"},
		{SystemProjectAgentID, "Agent"},
	}

	now := time.Now()
	for _, p := range systemProjects {
		_, err := s.db.Exec(`
			INSERT OR IGNORE INTO projects (id, name, folder, is_system, created_at, updated_at)
			VALUES (?, ?, NULL, 1, ?, ?)
		`, p.id, p.name, now, now)
		if err != nil {
			return fmt.Errorf("failed to seed system project %s: %w", p.id, err)
		}
	}
	return nil
}

// SaveSession saves a session to the database
func (s *SQLiteStore) SaveSession(sess *Session) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	metadata, _ := json.Marshal(sess.Metadata)

	// Upsert session
	_, err = tx.Exec(`
		INSERT INTO sessions (id, agent_id, parent_id, job_id, project_id, title, status, metadata, task_progress, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			parent_id = excluded.parent_id,
			job_id = excluded.job_id,
			project_id = excluded.project_id,
			title = excluded.title,
			status = excluded.status,
			metadata = excluded.metadata,
			task_progress = excluded.task_progress,
			updated_at = excluded.updated_at
	`, sess.ID, sess.AgentID, sess.ParentID, sess.JobID, sess.ProjectID, sess.Title, sess.Status, metadata, sess.TaskProgress, sess.CreatedAt, sess.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	// Delete existing messages and re-insert (simple approach for now)
	_, err = tx.Exec("DELETE FROM messages WHERE session_id = ?", sess.ID)
	if err != nil {
		return fmt.Errorf("failed to delete messages: %w", err)
	}

	// Insert messages
	for _, msg := range sess.Messages {
		messageMetadata, _ := json.Marshal(msg.Metadata)
		_, err = tx.Exec(`
			INSERT INTO messages (id, session_id, role, content, tool_calls, tool_results, metadata, timestamp)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, msg.ID, sess.ID, msg.Role, msg.Content, msg.ToolCalls, msg.ToolResults, messageMetadata, msg.Timestamp)
		if err != nil {
			return fmt.Errorf("failed to save message: %w", err)
		}
	}

	return tx.Commit()
}

// GetSession retrieves a session by ID
func (s *SQLiteStore) GetSession(id string) (*Session, error) {
	var sess Session
	var metadata sql.NullString
	var parentID sql.NullString
	var jobID sql.NullString
	var projectID sql.NullString
	var title sql.NullString
	var taskProgress sql.NullString

	err := s.db.QueryRow(`
		SELECT id, agent_id, parent_id, job_id, project_id, title, status, metadata, task_progress, created_at, updated_at
		FROM sessions WHERE id = ?
	`, id).Scan(&sess.ID, &sess.AgentID, &parentID, &jobID, &projectID, &title, &sess.Status, &metadata, &taskProgress, &sess.CreatedAt, &sess.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	if parentID.Valid {
		sess.ParentID = &parentID.String
	}
	if jobID.Valid {
		sess.JobID = &jobID.String
	}
	if projectID.Valid {
		sess.ProjectID = &projectID.String
	}
	if title.Valid {
		sess.Title = title.String
	}
	if metadata.Valid {
		json.Unmarshal([]byte(metadata.String), &sess.Metadata)
	}
	if taskProgress.Valid {
		sess.TaskProgress = taskProgress.String
	}

	// Load messages
	rows, err := s.db.Query(`
		SELECT id, role, content, tool_calls, tool_results, metadata, timestamp
		FROM messages WHERE session_id = ? ORDER BY timestamp
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var msg Message
		var toolCalls, toolResults, metadata sql.NullString

		err := rows.Scan(&msg.ID, &msg.Role, &msg.Content, &toolCalls, &toolResults, &metadata, &msg.Timestamp)
		if err != nil {
			return nil, err
		}

		if toolCalls.Valid {
			msg.ToolCalls = json.RawMessage(toolCalls.String)
		}
		if toolResults.Valid {
			msg.ToolResults = json.RawMessage(toolResults.String)
		}
		if metadata.Valid && metadata.String != "" {
			json.Unmarshal([]byte(metadata.String), &msg.Metadata)
		}

		sess.Messages = append(sess.Messages, msg)
	}

	return &sess, nil
}

// ListSessions lists all regular sessions plus Thinking job sessions.
func (s *SQLiteStore) ListSessions() ([]*Session, error) {
	rows, err := s.db.Query(`
		SELECT id, agent_id, parent_id, job_id, project_id, title, status, metadata, task_progress, created_at, updated_at
		FROM sessions 
		WHERE job_id IS NULL OR project_id = 'project-thinking'
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		var sess Session
		var parentID, jobID, projectID sql.NullString
		var title sql.NullString
		var metadata sql.NullString
		var taskProgress sql.NullString

		err := rows.Scan(&sess.ID, &sess.AgentID, &parentID, &jobID, &projectID, &title, &sess.Status, &metadata, &taskProgress, &sess.CreatedAt, &sess.UpdatedAt)
		if err != nil {
			return nil, err
		}

		if parentID.Valid {
			sess.ParentID = &parentID.String
		}
		if jobID.Valid {
			sess.JobID = &jobID.String
		}
		if projectID.Valid {
			sess.ProjectID = &projectID.String
		}
		if title.Valid {
			sess.Title = title.String
		}
		if metadata.Valid && metadata.String != "" {
			_ = json.Unmarshal([]byte(metadata.String), &sess.Metadata)
		}
		if taskProgress.Valid {
			sess.TaskProgress = taskProgress.String
		}

		sessions = append(sessions, &sess)
	}

	return sessions, nil
}

// ListSessionsByJob returns all sessions associated with a specific job
func (s *SQLiteStore) ListSessionsByJob(jobID string) ([]*Session, error) {
	rows, err := s.db.Query(`
		SELECT id, agent_id, parent_id, job_id, project_id, title, status, metadata, task_progress, created_at, updated_at
		FROM sessions 
		WHERE job_id = ?
		ORDER BY created_at DESC
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		var sess Session
		var parentID, jobID, projectID sql.NullString
		var title sql.NullString
		var metadata sql.NullString
		var taskProgress sql.NullString

		err := rows.Scan(&sess.ID, &sess.AgentID, &parentID, &jobID, &projectID, &title, &sess.Status, &metadata, &taskProgress, &sess.CreatedAt, &sess.UpdatedAt)
		if err != nil {
			return nil, err
		}

		if parentID.Valid {
			sess.ParentID = &parentID.String
		}
		if jobID.Valid {
			sess.JobID = &jobID.String
		}
		if projectID.Valid {
			sess.ProjectID = &projectID.String
		}
		if title.Valid {
			sess.Title = title.String
		}
		if metadata.Valid && metadata.String != "" {
			_ = json.Unmarshal([]byte(metadata.String), &sess.Metadata)
		}
		if taskProgress.Valid {
			sess.TaskProgress = taskProgress.String
		}

		sessions = append(sessions, &sess)
	}

	return sessions, nil
}

// DeleteSession deletes a session
func (s *SQLiteStore) DeleteSession(id string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	return err
}

// SaveProject saves a project to the database.
func (s *SQLiteStore) SaveProject(project *Project) error {
	_, err := s.db.Exec(`
		INSERT INTO projects (id, name, folder, is_system, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			folder = excluded.folder,
			is_system = excluded.is_system,
			updated_at = excluded.updated_at
	`, project.ID, project.Name, project.Folder, project.IsSystem, project.CreatedAt, project.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save project: %w", err)
	}

	return nil
}

// GetProject retrieves a project by ID.
func (s *SQLiteStore) GetProject(id string) (*Project, error) {
	var project Project
	var folder sql.NullString

	err := s.db.QueryRow(`
		SELECT id, name, folder, is_system, created_at, updated_at
		FROM projects
		WHERE id = ?
	`, id).Scan(&project.ID, &project.Name, &folder, &project.IsSystem, &project.CreatedAt, &project.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("project not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	if folder.Valid {
		project.Folder = &folder.String
	}

	return &project, nil
}

// ListProjects returns all projects ordered by name.
func (s *SQLiteStore) ListProjects() ([]*Project, error) {
	rows, err := s.db.Query(`
		SELECT id, name, folder, is_system, created_at, updated_at
		FROM projects
		ORDER BY name COLLATE NOCASE ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []*Project
	for rows.Next() {
		var project Project
		var folder sql.NullString
		if err := rows.Scan(&project.ID, &project.Name, &folder, &project.IsSystem, &project.CreatedAt, &project.UpdatedAt); err != nil {
			return nil, err
		}

		if folder.Valid {
			project.Folder = &folder.String
		}

		projects = append(projects, &project)
	}

	return projects, nil
}

// DeleteProject deletes a project and all associated sessions and their messages.
// System projects cannot be deleted.
func (s *SQLiteStore) DeleteProject(id string) error {
	// Check if this is a system project
	project, err := s.GetProject(id)
	if err != nil {
		return err
	}
	if project.IsSystem {
		return fmt.Errorf("cannot delete system project: %s", id)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete all sessions associated with this project (cascade deletes messages)
	if _, err := tx.Exec(`DELETE FROM sessions WHERE project_id = ?`, id); err != nil {
		return fmt.Errorf("failed to delete project sessions: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, id); err != nil {
		return fmt.Errorf("failed to delete project: %w", err)
	}

	return tx.Commit()
}

// Close closes the database connection
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// --- Recurring Jobs CRUD ---

// SaveJob saves a recurring job to the database
func (s *SQLiteStore) SaveJob(job *RecurringJob) error {
	_, err := s.db.Exec(`
		INSERT INTO recurring_jobs (id, name, schedule_human, schedule_cron, task_prompt, task_prompt_source, task_prompt_file, llm_provider, enabled, last_run_at, next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
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
	`, job.ID, job.Name, job.ScheduleHuman, job.ScheduleCron, job.TaskPrompt, job.TaskPromptSource, job.TaskPromptFile, job.LLMProvider, job.Enabled, job.LastRunAt, job.NextRunAt, job.CreatedAt, job.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save job: %w", err)
	}
	return nil
}

// GetJob retrieves a recurring job by ID
func (s *SQLiteStore) GetJob(id string) (*RecurringJob, error) {
	var job RecurringJob
	var lastRunAt, nextRunAt sql.NullTime
	var enabled int

	err := s.db.QueryRow(`
		SELECT id, name, schedule_human, schedule_cron, task_prompt, task_prompt_source, task_prompt_file, llm_provider, enabled, last_run_at, next_run_at, created_at, updated_at
		FROM recurring_jobs WHERE id = ?
	`, id).Scan(&job.ID, &job.Name, &job.ScheduleHuman, &job.ScheduleCron, &job.TaskPrompt, &job.TaskPromptSource, &job.TaskPromptFile, &job.LLMProvider, &enabled, &lastRunAt, &nextRunAt, &job.CreatedAt, &job.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("job not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	job.Enabled = enabled == 1
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
		SELECT id, name, schedule_human, schedule_cron, task_prompt, task_prompt_source, task_prompt_file, llm_provider, enabled, last_run_at, next_run_at, created_at, updated_at
		FROM recurring_jobs ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*RecurringJob
	for rows.Next() {
		var job RecurringJob
		var lastRunAt, nextRunAt sql.NullTime
		var enabled int

		err := rows.Scan(&job.ID, &job.Name, &job.ScheduleHuman, &job.ScheduleCron, &job.TaskPrompt, &job.TaskPromptSource, &job.TaskPromptFile, &job.LLMProvider, &enabled, &lastRunAt, &nextRunAt, &job.CreatedAt, &job.UpdatedAt)
		if err != nil {
			return nil, err
		}

		job.Enabled = enabled == 1
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
		SELECT id, name, schedule_human, schedule_cron, task_prompt, task_prompt_source, task_prompt_file, llm_provider, enabled, last_run_at, next_run_at, created_at, updated_at
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
		var lastRunAt, nextRunAt sql.NullTime
		var enabled int

		err := rows.Scan(&job.ID, &job.Name, &job.ScheduleHuman, &job.ScheduleCron, &job.TaskPrompt, &job.TaskPromptSource, &job.TaskPromptFile, &job.LLMProvider, &enabled, &lastRunAt, &nextRunAt, &job.CreatedAt, &job.UpdatedAt)
		if err != nil {
			return nil, err
		}

		job.Enabled = enabled == 1
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
			status = excluded.status,
			output = excluded.output,
			error = excluded.error,
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

// GetSettings returns all app settings as key/value pairs.
func (s *SQLiteStore) GetSettings() (map[string]string, error) {
	rows, err := s.db.Query(`
		SELECT key, value
		FROM app_settings
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = value
	}

	return settings, nil
}

// SaveSettings replaces all app settings with the provided map.
func (s *SQLiteStore) SaveSettings(settings map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM app_settings`); err != nil {
		return fmt.Errorf("failed to clear settings: %w", err)
	}

	now := time.Now()
	for key, value := range settings {
		if key == "" {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO app_settings (key, value, updated_at)
			VALUES (?, ?, ?)
		`, key, value, now); err != nil {
			return fmt.Errorf("failed to save setting %q: %w", key, err)
		}
	}

	return tx.Commit()
}

// SaveIntegration saves an integration to the database.
func (s *SQLiteStore) SaveIntegration(integration *Integration) error {
	if integration.Config == nil {
		integration.Config = map[string]string{}
	}

	configJSON, err := json.Marshal(integration.Config)
	if err != nil {
		return fmt.Errorf("failed to encode integration config: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO integrations (id, provider, name, mode, enabled, config, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			provider = excluded.provider,
			name = excluded.name,
			mode = excluded.mode,
			enabled = excluded.enabled,
			config = excluded.config,
			updated_at = excluded.updated_at
	`, integration.ID, integration.Provider, integration.Name, integration.Mode, integration.Enabled, string(configJSON), integration.CreatedAt, integration.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save integration: %w", err)
	}

	return nil
}

// GetIntegration returns an integration by id.
func (s *SQLiteStore) GetIntegration(id string) (*Integration, error) {
	var integration Integration
	var enabled int
	var configJSON string

	err := s.db.QueryRow(`
		SELECT id, provider, name, mode, enabled, config, created_at, updated_at
		FROM integrations
		WHERE id = ?
	`, id).Scan(
		&integration.ID,
		&integration.Provider,
		&integration.Name,
		&integration.Mode,
		&enabled,
		&configJSON,
		&integration.CreatedAt,
		&integration.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("integration not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	integration.Enabled = enabled == 1
	if configJSON != "" {
		if err := json.Unmarshal([]byte(configJSON), &integration.Config); err != nil {
			return nil, fmt.Errorf("failed to decode integration config: %w", err)
		}
	}
	if integration.Config == nil {
		integration.Config = map[string]string{}
	}

	return &integration, nil
}

// ListIntegrations returns all integrations ordered by creation date.
func (s *SQLiteStore) ListIntegrations() ([]*Integration, error) {
	rows, err := s.db.Query(`
		SELECT id, provider, name, mode, enabled, config, created_at, updated_at
		FROM integrations
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var integrations []*Integration
	for rows.Next() {
		var integration Integration
		var enabled int
		var configJSON string
		if err := rows.Scan(
			&integration.ID,
			&integration.Provider,
			&integration.Name,
			&integration.Mode,
			&enabled,
			&configJSON,
			&integration.CreatedAt,
			&integration.UpdatedAt,
		); err != nil {
			return nil, err
		}

		integration.Enabled = enabled == 1
		if configJSON != "" {
			if err := json.Unmarshal([]byte(configJSON), &integration.Config); err != nil {
				return nil, fmt.Errorf("failed to decode integration config: %w", err)
			}
		}
		if integration.Config == nil {
			integration.Config = map[string]string{}
		}

		integrations = append(integrations, &integration)
	}

	return integrations, nil
}

// DeleteIntegration deletes an integration by id.
func (s *SQLiteStore) DeleteIntegration(id string) error {
	_, err := s.db.Exec(`DELETE FROM integrations WHERE id = ?`, id)
	return err
}

// SaveMCPServer saves an MCP server to the database.
func (s *SQLiteStore) SaveMCPServer(server *MCPServer) error {
	if server.Config == nil {
		server.Config = map[string]string{}
	}

	configJSON, err := json.Marshal(server.Config)
	if err != nil {
		return fmt.Errorf("failed to encode mcp server config: %w", err)
	}

	var lastTestAt interface{}
	if server.LastTestAt != nil {
		lastTestAt = *server.LastTestAt
	}
	var lastTestSuccess interface{}
	if server.LastTestSuccess != nil {
		if *server.LastTestSuccess {
			lastTestSuccess = 1
		} else {
			lastTestSuccess = 0
		}
	}
	var lastEstimatedTokens interface{}
	if server.LastEstimatedTokens != nil {
		lastEstimatedTokens = *server.LastEstimatedTokens
	}
	var lastToolCount interface{}
	if server.LastToolCount != nil {
		lastToolCount = *server.LastToolCount
	}

	_, err = s.db.Exec(`
		INSERT INTO mcp_servers (id, name, transport, enabled, config, last_test_at, last_test_success, last_test_message, last_estimated_tokens, last_tool_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			transport = excluded.transport,
			enabled = excluded.enabled,
			config = excluded.config,
			last_test_at = excluded.last_test_at,
			last_test_success = excluded.last_test_success,
			last_test_message = excluded.last_test_message,
			last_estimated_tokens = excluded.last_estimated_tokens,
			last_tool_count = excluded.last_tool_count,
			updated_at = excluded.updated_at
	`, server.ID, server.Name, server.Transport, server.Enabled, string(configJSON), lastTestAt, lastTestSuccess, server.LastTestMessage, lastEstimatedTokens, lastToolCount, server.CreatedAt, server.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save mcp server: %w", err)
	}

	return nil
}

// GetMCPServer returns an MCP server by id.
func (s *SQLiteStore) GetMCPServer(id string) (*MCPServer, error) {
	var server MCPServer
	var enabled int
	var configJSON string
	var lastTestAt sql.NullTime
	var lastTestSuccess sql.NullInt64
	var lastTestMessage sql.NullString
	var lastEstimatedTokens sql.NullInt64
	var lastToolCount sql.NullInt64

	err := s.db.QueryRow(`
		SELECT id, name, transport, enabled, config, last_test_at, last_test_success, last_test_message, last_estimated_tokens, last_tool_count, created_at, updated_at
		FROM mcp_servers
		WHERE id = ?
	`, id).Scan(
		&server.ID,
		&server.Name,
		&server.Transport,
		&enabled,
		&configJSON,
		&lastTestAt,
		&lastTestSuccess,
		&lastTestMessage,
		&lastEstimatedTokens,
		&lastToolCount,
		&server.CreatedAt,
		&server.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("mcp server not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	server.Enabled = enabled == 1
	if lastTestAt.Valid {
		server.LastTestAt = &lastTestAt.Time
	}
	if lastTestSuccess.Valid {
		v := lastTestSuccess.Int64 == 1
		server.LastTestSuccess = &v
	}
	if lastTestMessage.Valid {
		server.LastTestMessage = lastTestMessage.String
	}
	if lastEstimatedTokens.Valid {
		v := int(lastEstimatedTokens.Int64)
		server.LastEstimatedTokens = &v
	}
	if lastToolCount.Valid {
		v := int(lastToolCount.Int64)
		server.LastToolCount = &v
	}
	if configJSON != "" {
		if err := json.Unmarshal([]byte(configJSON), &server.Config); err != nil {
			return nil, fmt.Errorf("failed to decode mcp server config: %w", err)
		}
	}
	if server.Config == nil {
		server.Config = map[string]string{}
	}

	return &server, nil
}

// ListMCPServers returns all MCP servers ordered by creation date.
func (s *SQLiteStore) ListMCPServers() ([]*MCPServer, error) {
	rows, err := s.db.Query(`
		SELECT id, name, transport, enabled, config, last_test_at, last_test_success, last_test_message, last_estimated_tokens, last_tool_count, created_at, updated_at
		FROM mcp_servers
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []*MCPServer
	for rows.Next() {
		var server MCPServer
		var enabled int
		var configJSON string
		var lastTestAt sql.NullTime
		var lastTestSuccess sql.NullInt64
		var lastTestMessage sql.NullString
		var lastEstimatedTokens sql.NullInt64
		var lastToolCount sql.NullInt64
		if err := rows.Scan(
			&server.ID,
			&server.Name,
			&server.Transport,
			&enabled,
			&configJSON,
			&lastTestAt,
			&lastTestSuccess,
			&lastTestMessage,
			&lastEstimatedTokens,
			&lastToolCount,
			&server.CreatedAt,
			&server.UpdatedAt,
		); err != nil {
			return nil, err
		}

		server.Enabled = enabled == 1
		if lastTestAt.Valid {
			server.LastTestAt = &lastTestAt.Time
		}
		if lastTestSuccess.Valid {
			v := lastTestSuccess.Int64 == 1
			server.LastTestSuccess = &v
		}
		if lastTestMessage.Valid {
			server.LastTestMessage = lastTestMessage.String
		}
		if lastEstimatedTokens.Valid {
			v := int(lastEstimatedTokens.Int64)
			server.LastEstimatedTokens = &v
		}
		if lastToolCount.Valid {
			v := int(lastToolCount.Int64)
			server.LastToolCount = &v
		}
		if configJSON != "" {
			if err := json.Unmarshal([]byte(configJSON), &server.Config); err != nil {
				return nil, fmt.Errorf("failed to decode mcp server config: %w", err)
			}
		}
		if server.Config == nil {
			server.Config = map[string]string{}
		}

		servers = append(servers, &server)
	}

	return servers, nil
}

// DeleteMCPServer deletes an MCP server by id.
func (s *SQLiteStore) DeleteMCPServer(id string) error {
	_, err := s.db.Exec(`DELETE FROM mcp_servers WHERE id = ?`, id)
	return err
}

// Ensure SQLiteStore implements Store
var _ Store = (*SQLiteStore)(nil)
