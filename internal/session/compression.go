package session

import (
	"fmt"

	"github.com/A2gent/brute/internal/storage"
)

// Compression data bypasses session Get/Save to avoid loading history or
// racing newer runtime state while persisting a tool original.
func (m *Manager) SaveCompressionEntries(sessionID string, entries []storage.CompressionEntry) error {
	store, ok := m.store.(storage.CompressionStore)
	if !ok {
		return fmt.Errorf("storage does not support compression entries")
	}
	return store.SaveCompressionEntries(sessionID, entries)
}

func (m *Manager) GetCompressionEntry(sessionID, hash string) (storage.CompressionEntry, error) {
	store, ok := m.store.(storage.CompressionStore)
	if !ok {
		return storage.CompressionEntry{}, fmt.Errorf("storage does not support compression entries")
	}
	return store.GetCompressionEntry(sessionID, hash)
}
