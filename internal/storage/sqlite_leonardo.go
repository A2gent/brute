package storage

import (
	"database/sql"
	"fmt"
	"time"
)

// SaveLeonardoGeneration upserts an async Leonardo generation record.
func (s *SQLiteStore) SaveLeonardoGeneration(generation *LeonardoGeneration) error {
	if generation == nil {
		return fmt.Errorf("generation is nil")
	}

	_, err := s.db.Exec(`
		INSERT INTO leonardo_generations (
			id, session_id, tool_call_id, integration_id, generation_id, status,
			prompt, request_json, response_json, error, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = excluded.session_id,
			tool_call_id = excluded.tool_call_id,
			integration_id = excluded.integration_id,
			generation_id = excluded.generation_id,
			status = excluded.status,
			prompt = excluded.prompt,
			request_json = excluded.request_json,
			response_json = excluded.response_json,
			error = excluded.error,
			updated_at = excluded.updated_at
	`, generation.ID, generation.SessionID, generation.ToolCallID, generation.IntegrationID, generation.GenerationID, generation.Status, generation.Prompt, generation.RequestJSON, generation.ResponseJSON, generation.Error, generation.CreatedAt, generation.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save leonardo generation: %w", err)
	}
	return nil
}

// GetLeonardoGenerationByGenerationID returns a Leonardo generation by provider generation ID.
func (s *SQLiteStore) GetLeonardoGenerationByGenerationID(generationID string) (*LeonardoGeneration, error) {
	var generation LeonardoGeneration

	err := s.db.QueryRow(`
		SELECT id, session_id, tool_call_id, integration_id, generation_id, status,
		       prompt, request_json, response_json, error, created_at, updated_at
		FROM leonardo_generations
		WHERE generation_id = ?
	`, generationID).Scan(
		&generation.ID,
		&generation.SessionID,
		&generation.ToolCallID,
		&generation.IntegrationID,
		&generation.GenerationID,
		&generation.Status,
		&generation.Prompt,
		&generation.RequestJSON,
		&generation.ResponseJSON,
		&generation.Error,
		&generation.CreatedAt,
		&generation.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("leonardo generation not found: %s", generationID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load leonardo generation: %w", err)
	}
	return &generation, nil
}

// ClaimLeonardoGenerationByGenerationID atomically transitions a generation
// from one status to another and returns the claimed row. This prevents
// duplicate webhook deliveries from processing the same generation in parallel.
func (s *SQLiteStore) ClaimLeonardoGenerationByGenerationID(generationID string, fromStatus string, toStatus string) (*LeonardoGeneration, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("failed to begin leonardo claim transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var generation LeonardoGeneration
	err = tx.QueryRow(`
		SELECT id, session_id, tool_call_id, integration_id, generation_id, status,
		       prompt, request_json, response_json, error, created_at, updated_at
		FROM leonardo_generations
		WHERE generation_id = ?
	`, generationID).Scan(
		&generation.ID,
		&generation.SessionID,
		&generation.ToolCallID,
		&generation.IntegrationID,
		&generation.GenerationID,
		&generation.Status,
		&generation.Prompt,
		&generation.RequestJSON,
		&generation.ResponseJSON,
		&generation.Error,
		&generation.CreatedAt,
		&generation.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, false, fmt.Errorf("leonardo generation not found: %s", generationID)
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to load leonardo generation for claim: %w", err)
	}

	if generation.Status != fromStatus {
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("failed to finalize leonardo claim transaction: %w", err)
		}
		return &generation, false, nil
	}

	now := time.Now()
	res, err := tx.Exec(`
		UPDATE leonardo_generations
		SET status = ?, updated_at = ?
		WHERE generation_id = ? AND status = ?
	`, toStatus, now, generationID, fromStatus)
	if err != nil {
		return nil, false, fmt.Errorf("failed to claim leonardo generation: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("failed to inspect leonardo claim result: %w", err)
	}
	if rowsAffected == 0 {
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("failed to finalize leonardo claim transaction: %w", err)
		}
		return &generation, false, nil
	}

	generation.Status = toStatus
	generation.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("failed to commit leonardo claim transaction: %w", err)
	}
	return &generation, true, nil
}
