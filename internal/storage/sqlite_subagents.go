package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// --- Sub-Agents CRUD ---

// SaveSubAgent saves a sub-agent to the database.
func (s *SQLiteStore) SaveSubAgent(sa *SubAgent) error {
	enabledToolsJSON, err := json.Marshal(sa.EnabledTools)
	if err != nil {
		return fmt.Errorf("failed to encode enabled tools: %w", err)
	}
	instrBlocks := sa.InstructionBlocks
	if instrBlocks == "" {
		instrBlocks = "[]"
	}

	_, err = s.db.Exec(`
		INSERT INTO sub_agents (id, name, project_id, provider, model, enabled_tools, instruction_blocks, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			project_id = excluded.project_id,
			provider = excluded.provider,
			model = excluded.model,
			enabled_tools = excluded.enabled_tools,
			instruction_blocks = excluded.instruction_blocks,
			updated_at = excluded.updated_at
	`, sa.ID, sa.Name, sa.ProjectID, sa.Provider, sa.Model, string(enabledToolsJSON), instrBlocks, sa.CreatedAt, sa.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save sub-agent: %w", err)
	}
	return nil
}

// GetSubAgent retrieves a sub-agent by ID.
func (s *SQLiteStore) GetSubAgent(id string) (*SubAgent, error) {
	var sa SubAgent
	var enabledToolsJSON string
	var projectID sql.NullString

	err := s.db.QueryRow(`
		SELECT id, name, project_id, provider, model, enabled_tools, instruction_blocks, created_at, updated_at
		FROM sub_agents WHERE id = ?
	`, id).Scan(&sa.ID, &sa.Name, &projectID, &sa.Provider, &sa.Model, &enabledToolsJSON, &sa.InstructionBlocks, &sa.CreatedAt, &sa.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("sub-agent not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	if projectID.Valid {
		trimmedProjectID := strings.TrimSpace(projectID.String)
		if trimmedProjectID != "" {
			sa.ProjectID = &trimmedProjectID
		}
	}

	if enabledToolsJSON != "" {
		if err := json.Unmarshal([]byte(enabledToolsJSON), &sa.EnabledTools); err != nil {
			return nil, fmt.Errorf("failed to decode enabled tools: %w", err)
		}
	}
	if sa.EnabledTools == nil {
		sa.EnabledTools = []string{}
	}

	return &sa, nil
}

// ListSubAgents returns all sub-agents ordered by name.
func (s *SQLiteStore) ListSubAgents() ([]*SubAgent, error) {
	rows, err := s.db.Query(`
		SELECT id, name, project_id, provider, model, enabled_tools, instruction_blocks, created_at, updated_at
		FROM sub_agents
		ORDER BY name COLLATE NOCASE ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*SubAgent
	for rows.Next() {
		var sa SubAgent
		var enabledToolsJSON string
		var projectID sql.NullString
		if err := rows.Scan(&sa.ID, &sa.Name, &projectID, &sa.Provider, &sa.Model, &enabledToolsJSON, &sa.InstructionBlocks, &sa.CreatedAt, &sa.UpdatedAt); err != nil {
			return nil, err
		}

		if projectID.Valid {
			trimmedProjectID := strings.TrimSpace(projectID.String)
			if trimmedProjectID != "" {
				sa.ProjectID = &trimmedProjectID
			}
		}

		if enabledToolsJSON != "" {
			if err := json.Unmarshal([]byte(enabledToolsJSON), &sa.EnabledTools); err != nil {
				return nil, fmt.Errorf("failed to decode enabled tools: %w", err)
			}
		}
		if sa.EnabledTools == nil {
			sa.EnabledTools = []string{}
		}

		agents = append(agents, &sa)
	}

	return agents, nil
}

// DeleteSubAgent deletes a sub-agent by ID.
func (s *SQLiteStore) DeleteSubAgent(id string) error {
	_, err := s.db.Exec(`DELETE FROM sub_agents WHERE id = ?`, id)
	return err
}
