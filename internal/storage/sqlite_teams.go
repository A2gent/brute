package storage

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrTeamNotFound identifies a missing persisted team.
var ErrTeamNotFound = errors.New("team not found")

// SaveTeam inserts or updates the SQLite index for a canonical YAML definition.
func (s *SQLiteStore) SaveTeam(team *TeamRecord) error {
	if team == nil {
		return fmt.Errorf("team is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO teams (id, project_id, name, description, definition_yaml, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			project_id = excluded.project_id,
			name = excluded.name,
			description = excluded.description,
			definition_yaml = excluded.definition_yaml,
			updated_at = excluded.updated_at
	`, team.ID, team.ProjectID, team.Name, team.Description, team.DefinitionYAML, team.CreatedAt, team.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save team: %w", err)
	}
	return nil
}

// GetTeam retrieves a team definition index by ID.
func (s *SQLiteStore) GetTeam(id string) (*TeamRecord, error) {
	var team TeamRecord
	err := s.db.QueryRow(`
		SELECT id, project_id, name, description, definition_yaml, created_at, updated_at
		FROM teams WHERE id = ?
	`, id).Scan(&team.ID, &team.ProjectID, &team.Name, &team.Description, &team.DefinitionYAML, &team.CreatedAt, &team.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrTeamNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get team: %w", err)
	}
	return &team, nil
}

// ListTeams returns global teams or teams for exactly one requested project.
func (s *SQLiteStore) ListTeams(projectID *string) ([]*TeamRecord, error) {
	query := `SELECT id, project_id, name, description, definition_yaml, created_at, updated_at FROM teams`
	args := []interface{}{}
	if projectID == nil {
		query += ` WHERE project_id = ''`
	} else {
		query += ` WHERE project_id = ?`
		args = append(args, *projectID)
	}
	query += ` ORDER BY name COLLATE NOCASE ASC, id ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list teams: %w", err)
	}
	defer rows.Close()

	teams := []*TeamRecord{}
	for rows.Next() {
		var team TeamRecord
		if err := rows.Scan(&team.ID, &team.ProjectID, &team.Name, &team.Description, &team.DefinitionYAML, &team.CreatedAt, &team.UpdatedAt); err != nil {
			return nil, err
		}
		teams = append(teams, &team)
	}
	return teams, rows.Err()
}

// DeleteTeam deletes the SQLite index row for a team.
func (s *SQLiteStore) DeleteTeam(id string) error {
	result, err := s.db.Exec(`DELETE FROM teams WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete team: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("%w: %s", ErrTeamNotFound, id)
	}
	return nil
}
