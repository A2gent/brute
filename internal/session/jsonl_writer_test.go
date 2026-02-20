package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// --- minimal in-memory Store stub ---

type memStore struct {
	sessions map[string]*storage.Session
}

func newMemStore() *memStore { return &memStore{sessions: make(map[string]*storage.Session)} }

func (m *memStore) SaveSession(s *storage.Session) error { m.sessions[s.ID] = s; return nil }
func (m *memStore) GetSession(id string) (*storage.Session, error) {
	s, ok := m.sessions[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return s, nil
}
func (m *memStore) ListSessions() ([]*storage.Session, error)            { return nil, nil }
func (m *memStore) ListSessionsByJob(string) ([]*storage.Session, error) { return nil, nil }
func (m *memStore) DeleteSession(string) error                           { return nil }
func (m *memStore) SaveProject(*storage.Project) error                   { return nil }
func (m *memStore) GetProject(string) (*storage.Project, error)          { return nil, nil }
func (m *memStore) ListProjects() ([]*storage.Project, error)            { return nil, nil }
func (m *memStore) DeleteProject(string) error                           { return nil }
func (m *memStore) SaveJob(*storage.RecurringJob) error                  { return nil }
func (m *memStore) GetJob(string) (*storage.RecurringJob, error)         { return nil, nil }
func (m *memStore) ListJobs() ([]*storage.RecurringJob, error)           { return nil, nil }
func (m *memStore) DeleteJob(string) error                               { return nil }
func (m *memStore) GetDueJobs(time.Time) ([]*storage.RecurringJob, error) {
	return nil, nil
}
func (m *memStore) SaveJobExecution(*storage.JobExecution) error { return nil }
func (m *memStore) GetJobExecution(string) (*storage.JobExecution, error) {
	return nil, nil
}
func (m *memStore) ListJobExecutions(string, int) ([]*storage.JobExecution, error) {
	return nil, nil
}
func (m *memStore) GetSettings() (map[string]string, error)    { return nil, nil }
func (m *memStore) SaveSettings(map[string]string) error       { return nil }
func (m *memStore) SaveIntegration(*storage.Integration) error { return nil }
func (m *memStore) GetIntegration(string) (*storage.Integration, error) {
	return nil, nil
}
func (m *memStore) ListIntegrations() ([]*storage.Integration, error) { return nil, nil }
func (m *memStore) DeleteIntegration(string) error                    { return nil }
func (m *memStore) SaveMCPServer(*storage.MCPServer) error            { return nil }
func (m *memStore) GetMCPServer(string) (*storage.MCPServer, error)   { return nil, nil }
func (m *memStore) ListMCPServers() ([]*storage.MCPServer, error)     { return nil, nil }
func (m *memStore) DeleteMCPServer(string) error                      { return nil }
func (m *memStore) Close() error                                      { return nil }

// --- helpers ---

func readJSONLFile(t *testing.T, path string) []JSONLRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open jsonl file: %v", err)
	}
	defer f.Close()

	var records []JSONLRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec JSONLRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal jsonl line %q: %v", line, err)
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan jsonl file: %v", err)
	}
	return records
}

// --- JSONLWriter unit tests ---

func TestJSONLWriter_Nil_IsNoop(t *testing.T) {
	var w *JSONLWriter
	sess := New("agent")
	sess.AddUserMessage("hello")
	if err := w.Flush(sess); err != nil {
		t.Fatalf("nil writer Flush should be a no-op, got: %v", err)
	}
}

func TestJSONLWriter_EmptyFolder_IsNoop(t *testing.T) {
	w := NewJSONLWriter("")
	if w != nil {
		t.Fatal("NewJSONLWriter(\"\") should return nil")
	}
}

func TestJSONLWriter_CreatesFileAndAppendsMessages(t *testing.T) {
	dir := t.TempDir()
	w := NewJSONLWriter(dir)

	sess := New("agent1")
	sess.AddUserMessage("first message")
	sess.AddAssistantMessage("hello back", nil)

	if err := w.Flush(sess); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	filePath := filepath.Join(dir, sess.ID+".jsonl")
	records := readJSONLFile(t, filePath)

	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].EventType != "message" {
		t.Errorf("record[0] event_type: got %q want %q", records[0].EventType, "message")
	}
	if records[0].Message.Role != "user" {
		t.Errorf("record[0] role: got %q want %q", records[0].Message.Role, "user")
	}
	if records[0].Message.Content != "first message" {
		t.Errorf("record[0] content: got %q want %q", records[0].Message.Content, "first message")
	}
	if records[1].Message.Role != "assistant" {
		t.Errorf("record[1] role: got %q want %q", records[1].Message.Role, "assistant")
	}
	if records[0].SessionID != sess.ID {
		t.Errorf("record session_id: got %q want %q", records[0].SessionID, sess.ID)
	}
}

func TestJSONLWriter_OnlyAppendsNewMessages(t *testing.T) {
	dir := t.TempDir()
	w := NewJSONLWriter(dir)

	sess := New("agent1")
	sess.AddUserMessage("msg1")

	// First flush — writes msg1
	if err := w.Flush(sess); err != nil {
		t.Fatalf("first Flush: %v", err)
	}

	// Add second message and flush again
	sess.AddAssistantMessage("msg2", nil)
	if err := w.Flush(sess); err != nil {
		t.Fatalf("second Flush: %v", err)
	}

	records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 2 {
		t.Fatalf("expected 2 records after two flushes, got %d", len(records))
	}

	// Third flush with no new messages — file must not grow
	if err := w.Flush(sess); err != nil {
		t.Fatalf("third Flush: %v", err)
	}
	records = readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 2 {
		t.Fatalf("expected still 2 records after no-op flush, got %d", len(records))
	}
}

func TestJSONLWriter_MultipleSessions_SeparateFiles(t *testing.T) {
	dir := t.TempDir()
	w := NewJSONLWriter(dir)

	s1 := New("agent1")
	s1.AddUserMessage("session one")

	s2 := New("agent2")
	s2.AddUserMessage("session two")

	if err := w.Flush(s1); err != nil {
		t.Fatalf("Flush s1: %v", err)
	}
	if err := w.Flush(s2); err != nil {
		t.Fatalf("Flush s2: %v", err)
	}

	r1 := readJSONLFile(t, filepath.Join(dir, s1.ID+".jsonl"))
	r2 := readJSONLFile(t, filepath.Join(dir, s2.ID+".jsonl"))

	if len(r1) != 1 || r1[0].Message.Content != "session one" {
		t.Errorf("s1 file wrong: %+v", r1)
	}
	if len(r2) != 1 || r2[0].Message.Content != "session two" {
		t.Errorf("s2 file wrong: %+v", r2)
	}
}

func TestJSONLWriter_SetFolder_ResetsCursors(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	w := NewJSONLWriter(dir1)

	sess := New("agent1")
	sess.AddUserMessage("msg1")

	if err := w.Flush(sess); err != nil {
		t.Fatalf("Flush to dir1: %v", err)
	}

	// Switch folder — cursor should reset, next flush re-writes all messages into dir2
	w.SetFolder(dir2)
	sess.AddAssistantMessage("msg2", nil)

	if err := w.Flush(sess); err != nil {
		t.Fatalf("Flush to dir2: %v", err)
	}

	// dir2 file should contain both messages (cursor was reset)
	records := readJSONLFile(t, filepath.Join(dir2, sess.ID+".jsonl"))
	if len(records) != 2 {
		t.Fatalf("dir2 expected 2 records after folder switch, got %d", len(records))
	}
}

func TestJSONLWriter_ToolCallsAndResults(t *testing.T) {
	dir := t.TempDir()
	w := NewJSONLWriter(dir)

	sess := New("agent1")
	sess.AddUserMessage("run a tool")
	sess.AddAssistantMessage("", []ToolCall{
		{ID: "tc1", Name: "bash", Input: json.RawMessage(`{"command":"echo hi"}`)},
	})
	sess.AddToolResult([]ToolResult{
		{ToolCallID: "tc1", Content: "hi\n"},
	})

	if err := w.Flush(sess); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}

	toolCallRec := records[1]
	if len(toolCallRec.Message.ToolCalls) == 0 {
		t.Fatal("expected tool calls in record[1]")
	}
	if toolCallRec.Message.ToolCalls[0].Name != "bash" {
		t.Errorf("tool call name: got %q want %q", toolCallRec.Message.ToolCalls[0].Name, "bash")
	}

	toolResultRec := records[2]
	if len(toolResultRec.Message.ToolResults) == 0 {
		t.Fatal("expected tool results in record[2]")
	}
	if toolResultRec.Message.ToolResults[0].Content != "hi\n" {
		t.Errorf("tool result content: got %q want %q", toolResultRec.Message.ToolResults[0].Content, "hi\n")
	}
}

func TestJSONLWriter_TimestampIsSet(t *testing.T) {
	dir := t.TempDir()
	w := NewJSONLWriter(dir)

	before := time.Now().Add(-time.Second)
	sess := New("agent1")
	sess.AddUserMessage("hello")
	after := time.Now().Add(time.Second)

	if err := w.Flush(sess); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) == 0 {
		t.Fatal("no records written")
	}
	ts := records[0].Timestamp
	if ts.Before(before) || ts.After(after) {
		t.Errorf("timestamp %v not in expected range [%v, %v]", ts, before, after)
	}
}

// --- Manager integration tests ---

func TestManager_Save_WritesJSONL(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(newMemStore())
	mgr.SetJSONLFolder(dir)

	sess := New("agent1")
	sess.AddUserMessage("hello from manager")

	if err := mgr.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Message.Content != "hello from manager" {
		t.Errorf("content: got %q", records[0].Message.Content)
	}
}

func TestManager_Save_AccumulatesAcrossMultipleSaves(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(newMemStore())
	mgr.SetJSONLFolder(dir)

	sess := New("agent1")
	sess.AddUserMessage("msg1")
	if err := mgr.Save(sess); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	sess.AddAssistantMessage("msg2", nil)
	if err := mgr.Save(sess); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
}

func TestManager_Save_NoJSONLWhenFolderNotSet(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(newMemStore())
	// no SetJSONLFolder call

	sess := New("agent1")
	sess.AddUserMessage("silent")
	if err := mgr.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No file should have been created
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected no files in dir, got %d", len(entries))
	}
}

func TestManager_SetJSONLFolder_LiveSwitch(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	mgr := NewManager(newMemStore())
	mgr.SetJSONLFolder(dir1)

	sess := New("agent1")
	sess.AddUserMessage("written to dir1")
	if err := mgr.Save(sess); err != nil {
		t.Fatalf("Save to dir1: %v", err)
	}

	// Switch folder mid-session
	mgr.SetJSONLFolder(dir2)
	sess.AddAssistantMessage("written to dir2", nil)
	if err := mgr.Save(sess); err != nil {
		t.Fatalf("Save to dir2: %v", err)
	}

	// dir1 has 1 record (the user message only)
	r1 := readJSONLFile(t, filepath.Join(dir1, sess.ID+".jsonl"))
	if len(r1) != 1 {
		t.Errorf("dir1: expected 1 record, got %d", len(r1))
	}

	// dir2 has 2 records (cursor reset on folder switch)
	r2 := readJSONLFile(t, filepath.Join(dir2, sess.ID+".jsonl"))
	if len(r2) != 2 {
		t.Errorf("dir2: expected 2 records after folder switch, got %d", len(r2))
	}
}

// --- Additional JSONLWriter tests ---

func TestJSONLWriter_ResetCursor_CausesRewrite(t *testing.T) {
	dir := t.TempDir()
	w := NewJSONLWriter(dir)

	sess := New("agent1")
	sess.AddUserMessage("msg1")

	if err := w.Flush(sess); err != nil {
		t.Fatalf("first Flush: %v", err)
	}

	// Without ResetCursor, a second flush of the same session appends nothing.
	if err := w.Flush(sess); err != nil {
		t.Fatalf("second Flush (no-op): %v", err)
	}
	records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 1 {
		t.Fatalf("expected 1 record before reset, got %d", len(records))
	}

	// After ResetCursor the same messages are written again.
	w.ResetCursor(sess.ID)
	if err := w.Flush(sess); err != nil {
		t.Fatalf("Flush after ResetCursor: %v", err)
	}
	records = readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 2 {
		t.Fatalf("expected 2 records after reset+flush, got %d", len(records))
	}
}

func TestJSONLWriter_NilResetCursor_IsNoop(t *testing.T) {
	var w *JSONLWriter
	w.ResetCursor("any-session-id") // must not panic
}

func TestJSONLWriter_SetFolder_SameFolder_PreservesCursor(t *testing.T) {
	dir := t.TempDir()
	w := NewJSONLWriter(dir)

	sess := New("agent1")
	sess.AddUserMessage("msg1")

	if err := w.Flush(sess); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Setting the same folder should NOT reset the cursor.
	w.SetFolder(dir)

	sess.AddAssistantMessage("msg2", nil)
	if err := w.Flush(sess); err != nil {
		t.Fatalf("Flush after same-folder SetFolder: %v", err)
	}

	records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 2 {
		t.Fatalf("expected 2 records (cursor preserved), got %d", len(records))
	}
}

func TestJSONLWriter_SetFolder_ToEmpty_DisablesWrites(t *testing.T) {
	dir := t.TempDir()
	w := NewJSONLWriter(dir)

	w.SetFolder("")

	sess := New("agent1")
	sess.AddUserMessage("should not be written")

	if err := w.Flush(sess); err != nil {
		t.Fatalf("Flush on disabled writer: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected no files after disabling writer, got %d", len(entries))
	}
}

func TestJSONLWriter_CursorRollback_OnWriteError(t *testing.T) {
	// Use a path that cannot be created (file in place of directory).
	tmpFile, err := os.CreateTemp("", "not-a-dir-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	// Use the temp file path AS the folder — MkdirAll will fail because a file
	// with that name already exists.
	w := NewJSONLWriter(tmpFile.Name())

	sess := New("agent1")
	sess.AddUserMessage("will fail to write")

	firstErr := w.Flush(sess)
	if firstErr == nil {
		t.Skip("expected write to fail but it succeeded (filesystem allows file-as-dir?)")
	}

	// Cursor should have been rolled back; a second Flush must attempt the same records.
	// We verify this by switching to a valid folder and confirming the record appears.
	dir := t.TempDir()
	w.SetFolder(dir)

	if err := w.Flush(sess); err != nil {
		t.Fatalf("Flush after rollback to valid folder: %v", err)
	}

	records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 1 {
		t.Fatalf("expected 1 record after rollback recovery, got %d", len(records))
	}
}

func TestJSONLWriter_ConcurrentFlush_IsDataRaceFree(t *testing.T) {
	dir := t.TempDir()
	w := NewJSONLWriter(dir)

	const goroutines = 8
	const msgsEach = 20

	sessions := make([]*Session, goroutines)
	for i := range sessions {
		sessions[i] = New("agent")
		for j := 0; j < msgsEach; j++ {
			sessions[i].AddUserMessage("msg")
		}
	}

	done := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func(s *Session) {
			defer func() { done <- struct{}{} }()
			for k := 0; k < 5; k++ {
				_ = w.Flush(s)
			}
		}(sessions[i])
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}

	// Each session file should have exactly msgsEach records (no duplicates, no loss).
	for _, sess := range sessions {
		records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
		if len(records) != msgsEach {
			t.Errorf("session %s: expected %d records, got %d", sess.ID, msgsEach, len(records))
		}
	}
}

// --- Additional Manager tests ---

func TestManager_SetJSONLFolder_Empty_DisablesWrites(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(newMemStore())
	mgr.SetJSONLFolder(dir)

	sess := New("agent1")
	sess.AddUserMessage("first")
	if err := mgr.Save(sess); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	// Disable writing
	mgr.SetJSONLFolder("")

	sess.AddAssistantMessage("second", nil)
	if err := mgr.Save(sess); err != nil {
		t.Fatalf("second Save after disable: %v", err)
	}

	// The file should still exist from the first Save, but must not have grown.
	records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 1 {
		t.Errorf("expected 1 record (writes disabled), got %d", len(records))
	}
}

func TestManager_SetJSONLFolder_ReenableAfterDisable(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(newMemStore())
	mgr.SetJSONLFolder(dir)

	sess := New("agent1")
	sess.AddUserMessage("msg1")
	if err := mgr.Save(sess); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	// Disable then re-enable with the same folder.
	mgr.SetJSONLFolder("")
	mgr.SetJSONLFolder(dir)

	sess.AddAssistantMessage("msg2", nil)
	if err := mgr.Save(sess); err != nil {
		t.Fatalf("Save after re-enable: %v", err)
	}

	// Because cursor was reset on folder switch (empty → dir), both messages land in the file.
	records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 3 {
		// dir1 file already has 1 from first Save; after re-enable the cursor reset means
		// both messages are re-written: 1 (original) + 2 (from reset) = 3 total lines.
		t.Errorf("expected 3 records after re-enable, got %d", len(records))
	}
}

func TestManager_SetJSONLWriter_Nil_DisablesWrites(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(newMemStore())
	mgr.SetJSONLFolder(dir)

	sess := New("agent1")
	sess.AddUserMessage("first")
	if err := mgr.Save(sess); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	// Nil out the writer entirely via SetJSONLWriter.
	mgr.SetJSONLWriter(nil)

	sess.AddAssistantMessage("second", nil)
	if err := mgr.Save(sess); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	// File must not have grown.
	records := readJSONLFile(t, filepath.Join(dir, sess.ID+".jsonl"))
	if len(records) != 1 {
		t.Errorf("expected 1 record after SetJSONLWriter(nil), got %d", len(records))
	}
}
