package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

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
		INSERT INTO mcp_servers (id, project_id, name, transport, enabled, config, last_test_at, last_test_success, last_test_message, last_estimated_tokens, last_tool_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			project_id = excluded.project_id,
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
	`, server.ID, nullableString(server.ProjectID), server.Name, server.Transport, server.Enabled, string(configJSON), lastTestAt, lastTestSuccess, server.LastTestMessage, lastEstimatedTokens, lastToolCount, server.CreatedAt, server.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save mcp server: %w", err)
	}

	return nil
}

// GetMCPServer returns an MCP server by id.
func (s *SQLiteStore) GetMCPServer(id string) (*MCPServer, error) {
	var server MCPServer
	var projectID sql.NullString
	var enabled int
	var configJSON string
	var lastTestAt sql.NullTime
	var lastTestSuccess sql.NullInt64
	var lastTestMessage sql.NullString
	var lastEstimatedTokens sql.NullInt64
	var lastToolCount sql.NullInt64

	err := s.db.QueryRow(`
		SELECT id, project_id, name, transport, enabled, config, last_test_at, last_test_success, last_test_message, last_estimated_tokens, last_tool_count, created_at, updated_at
		FROM mcp_servers
		WHERE id = ?
	`, id).Scan(
		&server.ID,
		&projectID,
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

	setNullableString(&server.ProjectID, projectID)
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
		SELECT id, project_id, name, transport, enabled, config, last_test_at, last_test_success, last_test_message, last_estimated_tokens, last_tool_count, created_at, updated_at
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
		var projectID sql.NullString
		var enabled int
		var configJSON string
		var lastTestAt sql.NullTime
		var lastTestSuccess sql.NullInt64
		var lastTestMessage sql.NullString
		var lastEstimatedTokens sql.NullInt64
		var lastToolCount sql.NullInt64
		if err := rows.Scan(
			&server.ID,
			&projectID,
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

		setNullableString(&server.ProjectID, projectID)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return servers, nil
}

// DeleteMCPServer deletes an MCP server by id.
func (s *SQLiteStore) DeleteMCPServer(id string) error {
	_, err := s.db.Exec(`DELETE FROM mcp_servers WHERE id = ?`, id)
	return err
}
