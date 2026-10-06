package storage

import (
	"strings"
	"time"
)

// Session archiving is a soft delete stored in session metadata so it needs no
// schema change and survives the regular SaveSession upsert path.
const (
	// SessionArchivedAtKey marks a completed session hidden from the main lists.
	SessionArchivedAtKey = "archived_at"
	// SessionArchiveAnalyzedAtKey marks an archived session already processed by
	// the archived-sessions review loop, so each session is analyzed once.
	SessionArchiveAnalyzedAtKey = "archive_analyzed_at"
)

// SessionMetadataTime parses an RFC3339 timestamp stored under key.
func SessionMetadataTime(metadata map[string]interface{}, key string) *time.Time {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata[key].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	return &parsed
}

// SetSessionMetadataTime stores value under key, or removes the key when value is nil.
func SetSessionMetadataTime(metadata map[string]interface{}, key string, value *time.Time) map[string]interface{} {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if value == nil {
		delete(metadata, key)
		return metadata
	}
	metadata[key] = value.UTC().Format(time.RFC3339Nano)
	return metadata
}
