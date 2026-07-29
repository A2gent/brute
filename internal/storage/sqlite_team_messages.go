package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrTeamMessageNotFound = errors.New("team message not found")

func (s *SQLiteStore) AppendTeamMessage(message *TeamMessage) error {
	if message == nil {
		return fmt.Errorf("team message is required")
	}
	toRoles, err := json.Marshal(message.ToRoles)
	if err != nil {
		return fmt.Errorf("failed to encode team message recipients: %w", err)
	}
	ccRoles, err := json.Marshal(message.CCRoles)
	if err != nil {
		return fmt.Errorf("failed to encode team message cc recipients: %w", err)
	}
	delivered, err := json.Marshal(message.Delivered)
	if err != nil {
		return fmt.Errorf("failed to encode team message deliveries: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO team_messages (
			id, team_run_id, thread_id, from_role, to_roles, cc_roles, kind,
			subject, body, expects_reply, created_at, delivered_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, message.ID, message.TeamRunID, message.ThreadID, message.FromRole, string(toRoles), string(ccRoles), message.Kind,
		message.Subject, message.Body, message.ExpectsReply, message.CreatedAt, string(delivered))
	if err != nil {
		return fmt.Errorf("failed to append team message: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetTeamMessage(runID, messageID string) (*TeamMessage, error) {
	row := s.db.QueryRow(`
		SELECT id, team_run_id, thread_id, from_role, to_roles, cc_roles, kind,
		       subject, body, expects_reply, created_at, delivered_json
		FROM team_messages WHERE team_run_id = ? AND id = ?
	`, runID, messageID)
	message, err := scanTeamMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrTeamMessageNotFound, messageID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get team message: %w", err)
	}
	return message, nil
}

func (s *SQLiteStore) ListTeamMessages(runID, after string, limit int) ([]*TeamMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := `
		SELECT id, team_run_id, thread_id, from_role, to_roles, cc_roles, kind,
		       subject, body, expects_reply, created_at, delivered_json
		FROM team_messages WHERE team_run_id = ?`
	args := []interface{}{runID}
	if after != "" {
		query += ` AND (created_at, id) > (
			SELECT created_at, id FROM team_messages WHERE team_run_id = ? AND id = ?
		)`
		args = append(args, runID, after)
	}
	query += ` ORDER BY created_at ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list team messages: %w", err)
	}
	defer rows.Close()
	messages := []*TeamMessage{}
	for rows.Next() {
		message, err := scanTeamMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *SQLiteStore) ListPendingTeamMessages(runID, role string, limit int) ([]*TeamMessage, error) {
	messages, err := s.ListTeamMessages(runID, "", 500)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	pending := make([]*TeamMessage, 0)
	for _, message := range messages {
		if !messageAddressesRole(message, role) {
			continue
		}
		if _, delivered := message.Delivered[role]; delivered {
			continue
		}
		pending = append(pending, message)
		if len(pending) == limit {
			break
		}
	}
	return pending, nil
}

// MarkTeamMessageDelivered keeps the first delivery timestamp so retries are idempotent.
func (s *SQLiteStore) MarkTeamMessageDelivered(runID, messageID, role string, deliveredAt time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var raw string
	err = tx.QueryRow(`SELECT delivered_json FROM team_messages WHERE team_run_id = ? AND id = ?`, runID, messageID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrTeamMessageNotFound, messageID)
	}
	if err != nil {
		return err
	}
	delivered := map[string]time.Time{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &delivered); err != nil {
			return fmt.Errorf("failed to decode team message deliveries: %w", err)
		}
	}
	if _, exists := delivered[role]; exists {
		return tx.Commit()
	}
	delivered[role] = deliveredAt
	encoded, err := json.Marshal(delivered)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE team_messages SET delivered_json = ? WHERE team_run_id = ? AND id = ?`, string(encoded), runID, messageID); err != nil {
		return err
	}
	return tx.Commit()
}

type teamMessageScanner interface {
	Scan(dest ...interface{}) error
}

func scanTeamMessage(scanner teamMessageScanner) (*TeamMessage, error) {
	var message TeamMessage
	var toRoles, ccRoles, delivered string
	if err := scanner.Scan(
		&message.ID, &message.TeamRunID, &message.ThreadID, &message.FromRole,
		&toRoles, &ccRoles, &message.Kind, &message.Subject, &message.Body,
		&message.ExpectsReply, &message.CreatedAt, &delivered,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(toRoles), &message.ToRoles); err != nil {
		return nil, fmt.Errorf("failed to decode team message recipients: %w", err)
	}
	if err := json.Unmarshal([]byte(ccRoles), &message.CCRoles); err != nil {
		return nil, fmt.Errorf("failed to decode team message cc recipients: %w", err)
	}
	message.Delivered = map[string]time.Time{}
	if delivered != "" {
		if err := json.Unmarshal([]byte(delivered), &message.Delivered); err != nil {
			return nil, fmt.Errorf("failed to decode team message deliveries: %w", err)
		}
	}
	return &message, nil
}

func messageAddressesRole(message *TeamMessage, role string) bool {
	if message == nil {
		return false
	}
	for _, target := range message.ToRoles {
		if target == role {
			return true
		}
	}
	for _, target := range message.CCRoles {
		if target == role {
			return true
		}
	}
	return false
}
