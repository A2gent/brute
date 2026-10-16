package session

import (
	"fmt"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// MarkArchiveAnalyzed routes tool writes through Save so catalog subscribers see
// the archive metadata change just like any other session write.
func (m *Manager) MarkArchiveAnalyzed(id string, at time.Time) error {
	sess, err := m.Get(id)
	if err != nil {
		return err
	}
	if storage.SessionMetadataTime(sess.Metadata, storage.SessionArchivedAtKey) == nil {
		return fmt.Errorf("session is not archived: %s", id)
	}
	sess.Metadata = storage.SetSessionMetadataTime(sess.Metadata, storage.SessionArchiveAnalyzedAtKey, &at)
	if at.After(sess.UpdatedAt) {
		sess.UpdatedAt = at
	}
	return m.Save(sess)
}
