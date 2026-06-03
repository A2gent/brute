package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

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
