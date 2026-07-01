package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

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
			summary TEXT DEFAULT '',
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
		// Migration: concise one-sentence session label for dense lists.
		`ALTER TABLE sessions ADD COLUMN summary TEXT DEFAULT ''`,
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
			project_id TEXT,
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
		`ALTER TABLE mcp_servers ADD COLUMN project_id TEXT`,
		`ALTER TABLE mcp_servers ADD COLUMN last_test_at TIMESTAMP`,
		`ALTER TABLE mcp_servers ADD COLUMN last_test_success INTEGER`,
		`ALTER TABLE mcp_servers ADD COLUMN last_test_message TEXT`,
		`ALTER TABLE mcp_servers ADD COLUMN last_estimated_tokens INTEGER`,
		`ALTER TABLE mcp_servers ADD COLUMN last_tool_count INTEGER`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_servers_project_id ON mcp_servers(project_id)`,
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
					slash_command TEXT NOT NULL DEFAULT '',
					content TEXT NOT NULL,
					created_at TIMESTAMP NOT NULL,
					updated_at TIMESTAMP NOT NULL
				)`,
		`ALTER TABLE session_templates ADD COLUMN slash_command TEXT NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_session_templates_slash_command ON session_templates(slash_command COLLATE NOCASE) WHERE slash_command <> ''`,
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
		`CREATE TABLE IF NOT EXISTS project_git_review_overlay_cache (
				project_id TEXT NOT NULL,
				repo_path TEXT NOT NULL DEFAULT '',
				branch TEXT NOT NULL,
				base_branch TEXT NOT NULL DEFAULT '',
				file_path TEXT NOT NULL,
				diff_hash TEXT NOT NULL,
				annotations_json TEXT NOT NULL DEFAULT '[]',
				created_at TIMESTAMP NOT NULL,
				updated_at TIMESTAMP NOT NULL,
				PRIMARY KEY(project_id, repo_path, branch, base_branch, file_path),
				FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
			)`,
		`CREATE INDEX IF NOT EXISTS idx_project_git_review_overlay_cache_project_id ON project_git_review_overlay_cache(project_id)`,
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
		shouldRun, err := s.shouldRunMigration(m)
		if err != nil {
			return fmt.Errorf("failed to inspect migration target: %w", err)
		}
		if !shouldRun {
			continue
		}
		// Ignore only duplicate-column ALTER errors so lock and syntax failures
		// still stop startup instead of leaving a partially migrated schema.
		_, err = s.db.Exec(m)
		if err != nil && !(strings.HasPrefix(m, "ALTER") && isSQLiteDuplicateColumnError(err)) {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	// Move legacy global project prompt options into project rows once the
	// settings column exists. Branch task documentation settings are project
	// scoped, so their old global env-style keys are removed after migration.
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

func (s *SQLiteStore) shouldRunMigration(statement string) (bool, error) {
	normalized := strings.TrimSpace(statement)
	fields := strings.Fields(normalized)
	if len(fields) == 0 {
		return false, nil
	}

	if len(fields) >= 6 &&
		strings.EqualFold(fields[0], "ALTER") &&
		strings.EqualFold(fields[1], "TABLE") &&
		strings.EqualFold(fields[3], "ADD") &&
		strings.EqualFold(fields[4], "COLUMN") {
		exists, err := s.columnExists(cleanSQLiteIdentifier(fields[2]), cleanSQLiteIdentifier(fields[5]))
		return !exists, err
	}

	if len(fields) >= 6 &&
		strings.EqualFold(fields[0], "CREATE") &&
		strings.EqualFold(fields[1], "TABLE") &&
		strings.EqualFold(fields[2], "IF") &&
		strings.EqualFold(fields[3], "NOT") &&
		strings.EqualFold(fields[4], "EXISTS") {
		exists, err := s.tableExists(cleanSQLiteIdentifier(fields[5]))
		return !exists, err
	}

	indexNamePos := -1
	if len(fields) >= 6 &&
		strings.EqualFold(fields[0], "CREATE") &&
		strings.EqualFold(fields[1], "INDEX") &&
		strings.EqualFold(fields[2], "IF") &&
		strings.EqualFold(fields[3], "NOT") &&
		strings.EqualFold(fields[4], "EXISTS") {
		indexNamePos = 5
	}
	if len(fields) >= 7 &&
		strings.EqualFold(fields[0], "CREATE") &&
		strings.EqualFold(fields[1], "UNIQUE") &&
		strings.EqualFold(fields[2], "INDEX") &&
		strings.EqualFold(fields[3], "IF") &&
		strings.EqualFold(fields[4], "NOT") &&
		strings.EqualFold(fields[5], "EXISTS") {
		indexNamePos = 6
	}
	if indexNamePos >= 0 {
		exists, err := s.indexExists(cleanSQLiteIdentifier(fields[indexNamePos]))
		return !exists, err
	}

	return true, nil
}

func (s *SQLiteStore) tableExists(name string) (bool, error) {
	return s.sqliteMasterObjectExists("table", name)
}

func (s *SQLiteStore) indexExists(name string) (bool, error) {
	return s.sqliteMasterObjectExists("index", name)
}

func (s *SQLiteStore) sqliteMasterObjectExists(objectType, name string) (bool, error) {
	if strings.TrimSpace(name) == "" {
		return false, nil
	}
	var found int
	err := s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type = ? AND name = ? LIMIT 1`, objectType, name).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLiteStore) columnExists(tableName, columnName string) (bool, error) {
	if strings.TrimSpace(tableName) == "" || strings.TrimSpace(columnName) == "" {
		return false, nil
	}
	rows, err := s.db.Query("PRAGMA table_info(" + quoteSQLiteIdentifier(tableName) + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if strings.EqualFold(name, columnName) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func cleanSQLiteIdentifier(identifier string) string {
	cleaned := strings.TrimSpace(identifier)
	cleaned = strings.TrimSuffix(cleaned, "(")
	cleaned = strings.Trim(cleaned, "`\"[]")
	if idx := strings.Index(cleaned, "("); idx >= 0 {
		cleaned = cleaned[:idx]
	}
	return strings.TrimSpace(cleaned)
}

func quoteSQLiteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func isSQLiteDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column name")
}
