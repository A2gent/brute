package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// PendingMessage is a user message queued while a session run was active.
// Images is an opaque JSON blob owned by the session package.
type PendingMessage struct {
	ID        string
	SessionID string
	Content   string
	Images    string
	CreatedAt time.Time
}

// PendingMessageStore is implemented by stores that can persist queued messages.
type PendingMessageStore interface {
	AddPendingMessage(msg *PendingMessage) error
	ListPendingMessages(sessionID string) ([]*PendingMessage, error)
	// TakePendingMessage atomically removes and returns one queued message.
	// It returns (nil, nil) when the message no longer exists.
	TakePendingMessage(sessionID, id string) (*PendingMessage, error)
	// TakeNextPendingMessage atomically removes and returns the oldest queued message.
	TakeNextPendingMessage(sessionID string) (*PendingMessage, error)
}

func (s *SQLiteStore) AddPendingMessage(msg *PendingMessage) error {
	images := msg.Images
	if images != "" {
		var raw interface{}
		if err := json.Unmarshal([]byte(images), &raw); err != nil {
			return err
		}
		metadata, changed, err := externalizeMessageImages(s.dataPath, msg.SessionID, map[string]interface{}{"images": raw})
		if err != nil {
			return err
		}
		if changed {
			encoded, err := json.Marshal(metadata["images"])
			if err != nil {
				return err
			}
			images = string(encoded)
		}
	}
	_, err := s.db.Exec(
		`INSERT INTO session_pending_messages (id, session_id, content, images, created_at) VALUES (?, ?, ?, ?, ?)`,
		msg.ID, msg.SessionID, msg.Content, images, msg.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to queue message: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListPendingMessages(sessionID string) ([]*PendingMessage, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, content, images, created_at FROM session_pending_messages WHERE session_id = ? ORDER BY seq ASC`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*PendingMessage
	for rows.Next() {
		var m PendingMessage
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Content, &m.Images, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) TakePendingMessage(sessionID, id string) (*PendingMessage, error) {
	return s.takePending(`WHERE session_id = ? AND id = ?`, sessionID, id)
}

func (s *SQLiteStore) TakeNextPendingMessage(sessionID string) (*PendingMessage, error) {
	return s.takePending(`WHERE session_id = ?`, sessionID)
}

func (s *SQLiteStore) takePending(where string, args ...interface{}) (*PendingMessage, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var seq int64
	var m PendingMessage
	err = tx.QueryRow(
		`SELECT seq, id, session_id, content, images, created_at FROM session_pending_messages `+where+` ORDER BY seq ASC LIMIT 1`,
		args...,
	).Scan(&seq, &m.ID, &m.SessionID, &m.Content, &m.Images, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM session_pending_messages WHERE seq = ?`, seq); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &m, nil
}
