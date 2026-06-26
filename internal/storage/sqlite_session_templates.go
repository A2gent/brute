package storage

import (
	"database/sql"
	"fmt"
)

// --- Session Templates CRUD ---

// SaveSessionTemplate saves a reusable session prompt template.
func (s *SQLiteStore) SaveSessionTemplate(template *SessionTemplate) error {
	_, err := s.db.Exec(`
		INSERT INTO session_templates (id, name, slash_command, content, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			slash_command = excluded.slash_command,
			content = excluded.content,
			updated_at = excluded.updated_at
	`, template.ID, template.Name, template.SlashCommand, template.Content, template.CreatedAt, template.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save session template: %w", err)
	}
	return nil
}

// GetSessionTemplate retrieves a session template by ID.
func (s *SQLiteStore) GetSessionTemplate(id string) (*SessionTemplate, error) {
	var template SessionTemplate
	err := s.db.QueryRow(`
		SELECT id, name, slash_command, content, created_at, updated_at
		FROM session_templates WHERE id = ?
	`, id).Scan(&template.ID, &template.Name, &template.SlashCommand, &template.Content, &template.CreatedAt, &template.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session template not found: %s", id)
	}
	if err != nil {
		return nil, err
	}
	return &template, nil
}

// ListSessionTemplates returns all session templates ordered by name.
func (s *SQLiteStore) ListSessionTemplates() ([]*SessionTemplate, error) {
	rows, err := s.db.Query(`
		SELECT id, name, slash_command, content, created_at, updated_at
		FROM session_templates
		ORDER BY name COLLATE NOCASE ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var templates []*SessionTemplate
	for rows.Next() {
		var template SessionTemplate
		if err := rows.Scan(&template.ID, &template.Name, &template.SlashCommand, &template.Content, &template.CreatedAt, &template.UpdatedAt); err != nil {
			return nil, err
		}
		templates = append(templates, &template)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return templates, nil
}

// DeleteSessionTemplate deletes a session template by ID.
func (s *SQLiteStore) DeleteSessionTemplate(id string) error {
	_, err := s.db.Exec(`DELETE FROM session_templates WHERE id = ?`, id)
	return err
}
