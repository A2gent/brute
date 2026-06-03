package storage

import (
	"database/sql"
	"fmt"
)

// SaveProject saves a project to the database.
func (s *SQLiteStore) SaveProject(project *Project) error {
	settingsJSON, err := marshalProjectSettings(project.Settings)
	if err != nil {
		return err
	}
	urlPatternsJSON, err := marshalProjectURLPatterns(project.URLPatterns)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		INSERT INTO projects (id, name, folder, settings, url_patterns, is_system, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			folder = excluded.folder,
			settings = excluded.settings,
			url_patterns = excluded.url_patterns,
			is_system = excluded.is_system,
			updated_at = excluded.updated_at
	`, project.ID, project.Name, project.Folder, settingsJSON, urlPatternsJSON, project.IsSystem, project.CreatedAt, project.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save project: %w", err)
	}

	return nil
}

// GetProject retrieves a project by ID.
func (s *SQLiteStore) GetProject(id string) (*Project, error) {
	var project Project
	var folder sql.NullString
	var settingsRaw sql.NullString
	var urlPatternsRaw sql.NullString

	err := s.db.QueryRow(`
		SELECT id, name, folder, settings, url_patterns, is_system, created_at, updated_at
		FROM projects
		WHERE id = ?
	`, id).Scan(&project.ID, &project.Name, &folder, &settingsRaw, &urlPatternsRaw, &project.IsSystem, &project.CreatedAt, &project.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("project not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	if folder.Valid {
		project.Folder = &folder.String
	}
	project.Settings = unmarshalProjectSettings(settingsRaw.String)
	project.URLPatterns = unmarshalProjectURLPatterns(urlPatternsRaw.String)

	return &project, nil
}

// ListProjects returns all projects ordered by name.
func (s *SQLiteStore) ListProjects() ([]*Project, error) {
	rows, err := s.db.Query(`
		SELECT id, name, folder, settings, url_patterns, is_system, created_at, updated_at
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
		var settingsRaw sql.NullString
		var urlPatternsRaw sql.NullString
		if err := rows.Scan(&project.ID, &project.Name, &folder, &settingsRaw, &urlPatternsRaw, &project.IsSystem, &project.CreatedAt, &project.UpdatedAt); err != nil {
			return nil, err
		}

		if folder.Valid {
			project.Folder = &folder.String
		}
		project.Settings = unmarshalProjectSettings(settingsRaw.String)
		project.URLPatterns = unmarshalProjectURLPatterns(urlPatternsRaw.String)

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
	if _, err := tx.Exec(`DELETE FROM job_executions WHERE job_id IN (SELECT id FROM recurring_jobs WHERE project_id = ?)`, id); err != nil {
		return fmt.Errorf("failed to delete project job executions: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM recurring_jobs WHERE project_id = ?`, id); err != nil {
		return fmt.Errorf("failed to delete project recurring jobs: %w", err)
	}
	// WHY: project deletion must not leave sub-agents pointing at a missing project.
	// WHAT: make those sub-agents global while preserving their configuration.
	if _, err := tx.Exec(`UPDATE sub_agents SET project_id = NULL WHERE project_id = ?`, id); err != nil {
		return fmt.Errorf("failed to clear sub-agent project bindings: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, id); err != nil {
		return fmt.Errorf("failed to delete project: %w", err)
	}

	return tx.Commit()
}
