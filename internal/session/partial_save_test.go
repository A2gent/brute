package session

import (
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestSavingPartiallyLoadedSessionKeepsTranscript(t *testing.T) {
	loaders := map[string]func(*Manager, string) (*Session, error){
		"list": func(m *Manager, id string) (*Session, error) {
			rows, err := m.List()
			if err != nil {
				return nil, err
			}
			return rows[0], nil
		},
		"list for display": func(m *Manager, id string) (*Session, error) {
			rows, err := m.ListForDisplay("")
			if err != nil {
				return nil, err
			}
			return rows[0], nil
		},
		"summary": (*Manager).GetSummary,
		"display without messages": func(m *Manager, id string) (*Session, error) {
			return m.GetForDisplay(id, false)
		},
		"older page cut off": func(m *Manager, id string) (*Session, error) {
			row, _, err := m.GetPageForDisplay(id, 1, "")
			return row, err
		},
	}

	for name, load := range loaders {
		t.Run(name, func(t *testing.T) {
			store, err := storage.NewSQLiteStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			manager := NewManager(store)
			sess, err := manager.Create("build")
			if err != nil {
				t.Fatal(err)
			}
			sess.AddUserMessage("first")
			sess.AddAssistantMessage("second", nil)
			sess.AddUserMessage("third")
			sess.SetStatus(StatusCompleted)
			if err := manager.Save(sess); err != nil {
				t.Fatal(err)
			}

			partial, err := load(manager, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			partial.SetStatus(StatusPaused)
			if err := manager.Save(partial); err != nil {
				t.Fatal(err)
			}

			got, err := manager.Get(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != StatusPaused {
				t.Fatalf("status = %s, want paused", got.Status)
			}
			if len(got.Messages) != 3 {
				t.Fatalf("transcript lost: %d messages left", len(got.Messages))
			}
		})
	}
}

func TestFullyLoadedSessionStillRemovesDroppedMessages(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store)
	sess, err := manager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("first")
	sess.AddUserMessage("second")
	if err := manager.Save(sess); err != nil {
		t.Fatal(err)
	}

	full, err := manager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	full.Messages = full.Messages[:1]
	if err := manager.Save(full); err != nil {
		t.Fatal(err)
	}

	got, err := manager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(got.Messages))
	}
}
