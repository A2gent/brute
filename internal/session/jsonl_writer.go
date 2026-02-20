package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// JSONLRecord is a single line written to the JSONL session log.
type JSONLRecord struct {
	SessionID string    `json:"session_id"`
	AgentID   string    `json:"agent_id,omitempty"`
	EventType string    `json:"event_type"` // "message"
	Timestamp time.Time `json:"timestamp"`
	Message   *Message  `json:"message,omitempty"`
}

// JSONLWriter appends session events to per-session JSONL files.
// Each session gets its own file: <folder>/<session_id>.jsonl
// Only newly added messages are written on each Flush call.
type JSONLWriter struct {
	mu     sync.Mutex
	folder string
	// cursor tracks how many messages have been written for each session file.
	cursor map[string]int
}

// NewJSONLWriter creates a writer that stores JSONL files in folder.
// Returns nil if folder is empty.
func NewJSONLWriter(folder string) *JSONLWriter {
	if folder == "" {
		return nil
	}
	return &JSONLWriter{
		folder: folder,
		cursor: make(map[string]int),
	}
}

// SetFolder updates the destination folder. Pass "" to disable writing.
func (w *JSONLWriter) SetFolder(folder string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.folder != folder {
		// Reset cursors so the next Flush re-writes from scratch into the new folder.
		w.cursor = make(map[string]int)
	}
	w.folder = folder
}

// Flush writes any messages that have not yet been written to disk for this session.
// It is safe to call concurrently. It is a no-op when the writer is disabled (folder == "").
// If the write fails the cursor is rolled back so that the next Flush retries the same records.
func (w *JSONLWriter) Flush(sess *Session) error {
	if w == nil {
		return nil
	}

	// Snapshot everything we need under the lock so no other goroutine can
	// interleave between reading the cursor and advancing it.
	w.mu.Lock()
	folder := w.folder
	if folder == "" {
		w.mu.Unlock()
		return nil
	}
	prevCursor := w.cursor[sess.ID]
	messages := sess.Messages // slice header copy; underlying array is immutable here
	newMessages := messages[prevCursor:]
	if len(newMessages) == 0 {
		w.mu.Unlock()
		return nil
	}
	// Build records while still holding the lock so the cursor advance is atomic
	// with respect to concurrent Flush calls for the same session.
	records := make([]JSONLRecord, len(newMessages))
	for i, msg := range newMessages {
		msgCopy := msg
		records[i] = JSONLRecord{
			SessionID: sess.ID,
			AgentID:   sess.AgentID,
			EventType: "message",
			Timestamp: msg.Timestamp,
			Message:   &msgCopy,
		}
	}
	// Advance cursor before releasing the lock so concurrent Flush calls for the
	// same session do not try to write the same records twice.
	w.cursor[sess.ID] = len(messages)
	w.mu.Unlock()

	// File I/O happens outside the lock. On any error we roll back the cursor
	// so the caller can retry (best-effort — concurrent writes may have already
	// advanced past our records, but we never skip further ahead).
	if writeErr := w.writeRecords(folder, sess.ID, records); writeErr != nil {
		w.mu.Lock()
		if w.cursor[sess.ID] == len(messages) {
			w.cursor[sess.ID] = prevCursor
		}
		w.mu.Unlock()
		return writeErr
	}
	return nil
}

// writeRecords performs the actual file I/O for a batch of records.
func (w *JSONLWriter) writeRecords(folder, sessionID string, records []JSONLRecord) error {
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return fmt.Errorf("jsonl_writer: mkdir %q: %w", folder, err)
	}

	filePath := filepath.Join(folder, sessionID+".jsonl")
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("jsonl_writer: open %q: %w", filePath, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("jsonl_writer: encode record: %w", err)
		}
	}
	return nil
}

// ResetCursor clears the written-message cursor for a session so the next Flush
// re-evaluates from scratch.
func (w *JSONLWriter) ResetCursor(sessionID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.cursor, sessionID)
}
