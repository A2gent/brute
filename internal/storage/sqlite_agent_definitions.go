package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

// --- Agent Definitions CRUD ---
// Stored agent definitions are local installations of unified agent YAML.
// Saved sub_agents are legacy rich configuration rows that execute as Docker
// definitions; this table holds imported docker/remote definitions plus their
// machine bindings.

// SaveAgentDefinition inserts or updates a stored agent definition.
func (s *SQLiteStore) SaveAgentDefinition(def *AgentDefinitionRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO agent_definitions (id, name, runtime, project_id, definition_yaml, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			runtime = excluded.runtime,
			project_id = excluded.project_id,
			definition_yaml = excluded.definition_yaml,
			updated_at = excluded.updated_at
	`, def.ID, def.Name, def.Runtime, nullableString(def.ProjectID), def.DefinitionYAML, def.CreatedAt, def.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save agent definition: %w", err)
	}
	return nil
}

// GetAgentDefinition retrieves a stored agent definition by ID.
func (s *SQLiteStore) GetAgentDefinition(id string) (*AgentDefinitionRecord, error) {
	var def AgentDefinitionRecord
	var projectID sql.NullString
	err := s.db.QueryRow(`
		SELECT id, name, runtime, project_id, definition_yaml, created_at, updated_at
		FROM agent_definitions WHERE id = ?
	`, id).Scan(&def.ID, &def.Name, &def.Runtime, &projectID, &def.DefinitionYAML, &def.CreatedAt, &def.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("agent definition not found: %s", id)
	}
	if err != nil {
		return nil, err
	}
	setAgentDefinitionProjectID(&def, projectID)
	return &def, nil
}

// ListAgentDefinitions returns all stored agent definitions ordered by name.
func (s *SQLiteStore) ListAgentDefinitions() ([]*AgentDefinitionRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, name, runtime, project_id, definition_yaml, created_at, updated_at
		FROM agent_definitions
		ORDER BY name COLLATE NOCASE ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var defs []*AgentDefinitionRecord
	for rows.Next() {
		var def AgentDefinitionRecord
		var projectID sql.NullString
		if err := rows.Scan(&def.ID, &def.Name, &def.Runtime, &projectID, &def.DefinitionYAML, &def.CreatedAt, &def.UpdatedAt); err != nil {
			return nil, err
		}
		setAgentDefinitionProjectID(&def, projectID)
		defs = append(defs, &def)
	}
	return defs, rows.Err()
}

func setAgentDefinitionProjectID(def *AgentDefinitionRecord, projectID sql.NullString) {
	if def == nil || !projectID.Valid {
		return
	}
	trimmed := strings.TrimSpace(projectID.String)
	if trimmed != "" {
		def.ProjectID = &trimmed
	}
}

// DeleteAgentDefinition deletes a stored agent definition by ID.
func (s *SQLiteStore) DeleteAgentDefinition(id string) error {
	_, err := s.db.Exec(`DELETE FROM agent_definitions WHERE id = ?`, id)
	return err
}
