// Package teamrun provides the persisted mailbox used by team runtimes.
package teamrun

import (
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/google/uuid"
)

const (
	MessageKindRequest   = "request"
	MessageKindReply     = "reply"
	MessageKindBroadcast = "broadcast"
	MessageKindStatus    = "status"
	MessageKindDone      = "done"
)

// Store is the persistence boundary needed by the mailbox bus.
type Store interface {
	AppendTeamMessage(message *storage.TeamMessage) error
	GetTeamMessage(runID, messageID string) (*storage.TeamMessage, error)
	ListTeamMessages(runID, after string, limit int) ([]*storage.TeamMessage, error)
	ListPendingTeamMessages(runID, role string, limit int) ([]*storage.TeamMessage, error)
	MarkTeamMessageDelivered(runID, messageID, role string, deliveredAt time.Time) error
}

// Bus owns envelope identities, reply correlation, and delivery state.
type Bus struct {
	store Store
}

func NewBus(store Store) *Bus {
	return &Bus{store: store}
}

func (b *Bus) Append(message *storage.TeamMessage) (*storage.TeamMessage, error) {
	if b == nil || b.store == nil {
		return nil, fmt.Errorf("team message store is unavailable")
	}
	if message == nil {
		return nil, fmt.Errorf("team message is required")
	}
	message.TeamRunID = strings.TrimSpace(message.TeamRunID)
	message.FromRole = strings.TrimSpace(message.FromRole)
	message.ToRoles = uniqueRoles(message.ToRoles)
	message.CCRoles = uniqueRolesExcluding(message.CCRoles, message.ToRoles)
	message.Kind = strings.TrimSpace(message.Kind)
	message.Subject = strings.TrimSpace(message.Subject)
	message.Body = strings.TrimSpace(message.Body)
	if message.TeamRunID == "" || message.FromRole == "" {
		return nil, fmt.Errorf("team_run_id and from role are required")
	}
	if len(message.ToRoles) == 0 && len(message.CCRoles) == 0 && message.Kind != MessageKindDone {
		return nil, fmt.Errorf("at least one recipient is required")
	}
	if !validMessageKind(message.Kind) {
		return nil, fmt.Errorf("unsupported team message kind %q", message.Kind)
	}
	if message.Body == "" {
		return nil, fmt.Errorf("team message body is required")
	}
	if message.ID == "" {
		message.ID = "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if message.ThreadID == "" {
		message.ThreadID = "thr_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	if message.Delivered == nil {
		message.Delivered = map[string]time.Time{}
	}
	if err := b.store.AppendTeamMessage(message); err != nil {
		return nil, err
	}
	return message, nil
}

func (b *Bus) Reply(runID, messageID, fromRole, body string) (*storage.TeamMessage, error) {
	original, err := b.store.GetTeamMessage(runID, messageID)
	if err != nil {
		return nil, err
	}
	fromRole = strings.TrimSpace(fromRole)
	if fromRole == "" {
		return nil, fmt.Errorf("from role is required")
	}
	return b.Append(&storage.TeamMessage{
		TeamRunID: runID,
		ThreadID:  original.ThreadID,
		FromRole:  fromRole,
		ToRoles:   []string{original.FromRole},
		Kind:      MessageKindReply,
		Subject:   original.Subject,
		Body:      body,
	})
}

func (b *Bus) List(runID, after string, limit int) ([]*storage.TeamMessage, error) {
	return b.store.ListTeamMessages(runID, after, limit)
}

func (b *Bus) Pending(runID, role string, limit int) ([]*storage.TeamMessage, error) {
	return b.store.ListPendingTeamMessages(runID, strings.TrimSpace(role), limit)
}

func (b *Bus) MarkDelivered(runID, messageID, role string, deliveredAt time.Time) error {
	if deliveredAt.IsZero() {
		deliveredAt = time.Now().UTC()
	}
	return b.store.MarkTeamMessageDelivered(runID, messageID, strings.TrimSpace(role), deliveredAt)
}

func validMessageKind(kind string) bool {
	switch kind {
	case MessageKindRequest, MessageKindReply, MessageKindBroadcast, MessageKindStatus, MessageKindDone:
		return true
	default:
		return false
	}
}

func uniqueRoles(roles []string) []string {
	return uniqueRolesExcluding(roles, nil)
}

func uniqueRolesExcluding(roles, excluded []string) []string {
	seen := make(map[string]struct{}, len(roles)+len(excluded))
	for _, role := range excluded {
		seen[role] = struct{}{}
	}
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role == "" {
			continue
		}
		if _, exists := seen[role]; exists {
			continue
		}
		seen[role] = struct{}{}
		out = append(out, role)
	}
	return out
}
