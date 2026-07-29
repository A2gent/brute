package teamrun

import (
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func newBusTestStore(t *testing.T) *storage.SQLiteStore {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SaveTeamRun(&storage.TeamRun{
		ID:         "run-1",
		TeamID:     "team-1",
		SessionID:  "parent-1",
		Status:     storage.TeamRunStatusRunning,
		PolicyJSON: `{}`,
		StartedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveTeamRun() error = %v", err)
	}
	return store
}

func TestBusFanOutPendingAndIdempotentDelivery(t *testing.T) {
	store := newBusTestStore(t)
	bus := NewBus(store)
	created := time.Now().UTC().Truncate(time.Second)

	message, err := bus.Append(&storage.TeamMessage{
		ID:           "msg-1",
		TeamRunID:    "run-1",
		FromRole:     "architect",
		ToRoles:      []string{"developer"},
		CCRoles:      []string{"critic", "developer"},
		Kind:         MessageKindRequest,
		Subject:      "Implement retry",
		Body:         "Please add bounded retry.",
		ExpectsReply: true,
		CreatedAt:    created,
	})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if message.ID != "msg-1" || message.ThreadID == "" {
		t.Fatalf("Append() = %#v", message)
	}

	for _, role := range []string{"developer", "critic"} {
		pending, err := bus.Pending("run-1", role, 10)
		if err != nil {
			t.Fatalf("Pending(%s) error = %v", role, err)
		}
		if len(pending) != 1 || pending[0].ID != message.ID {
			t.Fatalf("Pending(%s) = %#v", role, pending)
		}
	}
	if pending, err := bus.Pending("run-1", "architect", 10); err != nil || len(pending) != 0 {
		t.Fatalf("Pending(sender) = %#v, %v", pending, err)
	}

	deliveredAt := created.Add(time.Second)
	if err := bus.MarkDelivered("run-1", message.ID, "developer", deliveredAt); err != nil {
		t.Fatalf("MarkDelivered() error = %v", err)
	}
	if err := bus.MarkDelivered("run-1", message.ID, "developer", deliveredAt.Add(time.Minute)); err != nil {
		t.Fatalf("MarkDelivered(idempotent) error = %v", err)
	}
	pending, err := bus.Pending("run-1", "developer", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("Pending(delivered developer) = %#v, %v", pending, err)
	}
	listed, err := bus.List("run-1", "", 10)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List() = %#v, %v", listed, err)
	}
	if got := listed[0].Delivered["developer"]; !got.Equal(deliveredAt) {
		t.Fatalf("first delivered timestamp = %v, want %v", got, deliveredAt)
	}
}

func TestBusReplyPropagatesThreadAndTargetsSender(t *testing.T) {
	store := newBusTestStore(t)
	bus := NewBus(store)

	request, err := bus.Append(&storage.TeamMessage{
		ID:        "request-1",
		TeamRunID: "run-1",
		FromRole:  "architect",
		ToRoles:   []string{"developer"},
		Kind:      MessageKindRequest,
		Subject:   "Uploader retry",
		Body:      "Please implement it.",
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Append(request) error = %v", err)
	}
	reply, err := bus.Reply("run-1", request.ID, "developer", "Implemented with tests.")
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if reply.ThreadID != request.ThreadID || len(reply.ToRoles) != 1 || reply.ToRoles[0] != "architect" || reply.Kind != MessageKindReply {
		t.Fatalf("Reply() = %#v, request = %#v", reply, request)
	}
	if reply.Subject != request.Subject {
		t.Fatalf("reply subject = %q, want %q", reply.Subject, request.Subject)
	}
}
