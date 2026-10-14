package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// GetLiveState returns runtime fields and messages newer than the known tail or
// flagged injected_during_run. Stores without incremental reads retain the old
// full-read behavior for compatibility.
func (m *Manager) GetLiveState(id, afterMessageID string) (*Session, error) {
	if store, ok := m.store.(interface {
		GetSessionLiveState(string, string) (*storage.Session, error)
	}); ok {
		row, err := store.GetSessionLiveState(id, afterMessageID)
		if err != nil {
			return nil, err
		}
		return FromStorage(row), nil
	}
	row, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	start := len(row.Messages)
	if afterMessageID == "" {
		start = 0
	} else {
		for i, message := range row.Messages {
			if message.ID == afterMessageID {
				start = i + 1
				break
			}
		}
	}
	messages := make([]Message, 0)
	for i, message := range row.Messages {
		injected := false
		if message.Role == "user" {
			switch value := message.Metadata["injected_during_run"].(type) {
			case bool:
				injected = value
			case string:
				injected = strings.EqualFold(strings.TrimSpace(value), "true")
			}
		}
		if i >= start || injected {
			messages = append(messages, message)
		}
	}
	row.Messages = messages
	return row, nil
}

// SaveMessageMetadataKey persists only one message metadata key, leaving
// concurrently edited session fields and newly injected messages untouched.
func (m *Manager) SaveMessageMetadataKey(sessionID, messageID, key string, value interface{}, updatedAt time.Time) error {
	if store, ok := m.store.(interface {
		UpdateMessageMetadataKey(string, string, string, interface{}, time.Time) error
	}); ok {
		return store.UpdateMessageMetadataKey(sessionID, messageID, key, value, updatedAt)
	}
	sess, err := m.Get(sessionID)
	if err != nil {
		return err
	}
	for i := range sess.Messages {
		if sess.Messages[i].ID != messageID {
			continue
		}
		if sess.Messages[i].Metadata == nil {
			sess.Messages[i].Metadata = make(map[string]interface{})
		}
		sess.Messages[i].Metadata[key] = value
		sess.MarkMessageChanged(messageID)
		return m.Save(sess)
	}
	return fmt.Errorf("message not found: %s", messageID)
}
