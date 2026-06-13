package storage

import "fmt"

// Migrations stay isolated so schema changes remain easy to review without touching CRUD code.

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
				project_id TEXT,
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
		`ALTER TABLE recurring_jobs ADD COLUMN project_id TEXT`,
		`ALTER TABLE recurring_jobs ADD COLUMN task_prompt_source TEXT NOT NULL DEFAULT 'text'`,
		`ALTER TABLE recurring_jobs ADD COLUMN task_prompt_file TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE recurring_jobs ADD COLUMN llm_provider TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_recurring_jobs_project_id ON recurring_jobs(project_id)`,
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
		// Leonardo async generations
		`CREATE TABLE IF NOT EXISTS leonardo_generations (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			tool_call_id TEXT NOT NULL,
			integration_id TEXT NOT NULL,
			generation_id TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL,
			prompt TEXT NOT NULL DEFAULT '',
			request_json TEXT NOT NULL DEFAULT '',
			response_json TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
			FOREIGN KEY (integration_id) REFERENCES integrations(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_leonardo_generations_generation_id ON leonardo_generations(generation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_leonardo_generations_session_id ON leonardo_generations(session_id)`,
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
		// Project-scoped settings hold per-project prompt assembly options.
		`ALTER TABLE projects ADD COLUMN settings TEXT NOT NULL DEFAULT '{}'`,
		// Browser-extension project auto-detection patterns. Stored as JSON to keep
		// the project table compact and preserve the existing CRUD interface.
		`ALTER TABLE projects ADD COLUMN url_patterns TEXT NOT NULL DEFAULT '[]'`,
		// Migration: Add task_progress column to sessions
		`ALTER TABLE sessions ADD COLUMN task_progress TEXT`,
		// Session templates are reusable prompt snippets for pre-filling new sessions.
		`CREATE TABLE IF NOT EXISTS session_templates (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				content TEXT NOT NULL,
				created_at TIMESTAMP NOT NULL,
				updated_at TIMESTAMP NOT NULL
			)`,
		`CREATE INDEX IF NOT EXISTS idx_session_templates_name ON session_templates(name COLLATE NOCASE)`,
		// Sub-agents table
		`CREATE TABLE IF NOT EXISTS sub_agents (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				project_id TEXT,
				provider TEXT NOT NULL DEFAULT '',
				model TEXT NOT NULL DEFAULT '',
				enabled_tools TEXT NOT NULL DEFAULT '[]',
				created_at TIMESTAMP NOT NULL,
				updated_at TIMESTAMP NOT NULL
			)`,
		// Migration: add optional project binding and instruction blocks to sub_agents.
		`ALTER TABLE sub_agents ADD COLUMN instruction_blocks TEXT NOT NULL DEFAULT '[]'`,
		// Project Databases
		`CREATE TABLE IF NOT EXISTS project_databases (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			name TEXT NOT NULL,
			engine TEXT NOT NULL,
			dsn TEXT NOT NULL,
			environment TEXT NOT NULL,
			is_read_only INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_project_databases_project_id ON project_databases(project_id)`,
		`ALTER TABLE sub_agents ADD COLUMN project_id TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_sub_agents_project_id ON sub_agents(project_id)`,
		`CREATE TABLE IF NOT EXISTS project_pr_descriptions (
			project_id TEXT NOT NULL,
			repo_path TEXT NOT NULL DEFAULT '',
			branch TEXT NOT NULL,
			base_branch TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY(project_id, repo_path, branch, base_branch),
			FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_project_pr_descriptions_project_id ON project_pr_descriptions(project_id)`,
		`CREATE TABLE IF NOT EXISTS project_test_cache (
			project_id TEXT NOT NULL,
			repo_path TEXT NOT NULL DEFAULT '',
			branch TEXT NOT NULL,
			base_branch TEXT NOT NULL DEFAULT '',
			scope_hash TEXT NOT NULL,
			test_response TEXT NOT NULL DEFAULT '',
			coverage_response TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY(project_id, repo_path, branch, base_branch, scope_hash),
			FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_project_test_cache_project_id ON project_test_cache(project_id)`,
		// Stored unified agent definitions (docker/remote runtime installations).
		`CREATE TABLE IF NOT EXISTS agent_definitions (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			runtime TEXT NOT NULL,
			definition_yaml TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_definitions_runtime ON agent_definitions(runtime)`,
	}

	for _, m := range migrations {
		// Ignore errors for ALTER TABLE (column may already exist)
		_, err := s.db.Exec(m)
		if err != nil && m[:5] != "ALTER" {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	// Move legacy global project prompt options into project rows once the
	// settings column exists. The source app settings are left intact so older
	// builds can still read them if users roll back.
	if err := s.migrateProjectPromptSettingsFromAppSettings(); err != nil {
		return fmt.Errorf("failed to migrate project prompt settings: %w", err)
	}

	// Seed system projects (idempotent - uses INSERT OR IGNORE)
	if err := s.seedSystemProjects(); err != nil {
		return fmt.Errorf("failed to seed system projects: %w", err)
	}
	if err := s.seedBuiltInSubAgents(); err != nil {
		return fmt.Errorf("failed to seed built-in sub-agents: %w", err)
	}

	return nil
}
