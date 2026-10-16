package session

import (
	"sync"

	"github.com/A2gent/brute/internal/storage"
)

// ChangeKind classifies a committed session write.
type ChangeKind int

const (
	ChangeUpdated ChangeKind = iota
	ChangeCreated
	ChangeDeleted
)

// Change describes a committed session write. Session is the state that was
// just saved; it is nil for deletes and column-only writes (listeners reload).
type Change struct {
	Kind      ChangeKind
	ID        string
	ProjectID string
	Session   *Session
}

// sessionTreeLister is implemented by stores that can resolve the rows removed
// by a cascading delete before the delete runs.
type sessionTreeLister interface {
	ListSessionTree(rootID string) ([]storage.SessionRef, error)
}

// SetChangeListener registers the single post-commit hook. WHY: the sessions
// catalog must see every persisted change (status, tokens, metadata, task
// progress) without each caller remembering to publish an event.
func (m *Manager) SetChangeListener(fn func(Change)) {
	m.changeMu.Lock()
	m.onChange = fn
	m.changeMu.Unlock()
}

func (m *Manager) changeListener() func(Change) {
	m.changeMu.RLock()
	defer m.changeMu.RUnlock()
	return m.onChange
}

type sessionWriteLock struct {
	mu   sync.Mutex
	refs int // Holders and waiters, guarded by Manager.writeMu.
}

// lockSession serializes commit+notify. Retire idle locks (including after Delete)
// only when no waiter retains them, otherwise a new mutex could bypass the queue.
func (m *Manager) lockSession(id string) func() {
	m.writeMu.Lock()
	value, _ := m.writeLocks.LoadOrStore(id, &sessionWriteLock{})
	lock := value.(*sessionWriteLock)
	lock.refs++
	m.writeMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		m.writeMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			m.writeLocks.Delete(id)
		}
		m.writeMu.Unlock()
	}
}

func (m *Manager) notify(change Change) {
	if fn := m.changeListener(); fn != nil {
		fn(change)
	}
}

// createAndNotify persists a newly built session and reports it as created.
func (m *Manager) createAndNotify(sess *Session) error {
	unlock := m.lockSession(sess.ID)
	defer unlock()
	if err := m.saveImages(sess); err != nil {
		return err
	}
	if err := m.store.SaveSession(sess.ToStorage()); err != nil {
		return err
	}
	m.notify(Change{Kind: ChangeCreated, ID: sess.ID, Session: sess})
	return nil
}

// deletedSubtree resolves rows a cascading delete will remove. It is only
// queried for notifications or attachment cleanup; stores without the query report the root.
func (m *Manager) deletedSubtree(id string) []Change {
	if m.changeListener() == nil && m.ImageRoot() == "" {
		return nil
	}
	if lister, ok := m.store.(sessionTreeLister); ok {
		if refs, err := lister.ListSessionTree(id); err == nil && len(refs) > 0 {
			changes := make([]Change, len(refs))
			for i, ref := range refs {
				changes[i] = Change{Kind: ChangeDeleted, ID: ref.ID, ProjectID: ref.ProjectID}
			}
			return changes
		}
	}
	change := Change{Kind: ChangeDeleted, ID: id}
	if summary, err := m.store.GetSessionSummary(id); err == nil && summary.ProjectID != nil {
		change.ProjectID = *summary.ProjectID
	}
	return []Change{change}
}
