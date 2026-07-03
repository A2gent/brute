package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestProjectSessionHistoryToolListScopesToCurrentProject(t *testing.T) {
	projectID := "project-a"
	otherProjectID := "project-b"
	store := newMockProjectSessionHistoryStore([]*storage.Session{
		projectHistorySession("current", projectID, "Current", "running", "", time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)),
		projectHistorySession("past-a", projectID, "Past A", "completed", "Relevant work", time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)),
		projectHistorySession("other", otherProjectID, "Other Project", "completed", "Must not leak", time.Date(2026, 1, 4, 10, 0, 0, 0, time.UTC)),
	})
	tool := NewProjectSessionHistoryTool(store)

	result, err := tool.Execute(context.WithValue(context.Background(), "session_id", "current"), mustJSON(t, map[string]any{
		"action": "list",
		"limit":  10,
	}))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}
	if !strings.Contains(result.Output, "past-a") {
		t.Fatalf("expected project session in output: %s", result.Output)
	}
	if strings.Contains(result.Output, "current") {
		t.Fatalf("current session should be excluded by default: %s", result.Output)
	}
	if strings.Contains(result.Output, "other") || strings.Contains(result.Output, "Must not leak") {
		t.Fatalf("other project data leaked: %s", result.Output)
	}
}

func TestProjectSessionHistoryToolGetRejectsOutsideProject(t *testing.T) {
	projectID := "project-a"
	otherProjectID := "project-b"
	store := newMockProjectSessionHistoryStore([]*storage.Session{
		projectHistorySession("current", projectID, "Current", "running", "", time.Now()),
		projectHistorySession("other", otherProjectID, "Other Project", "completed", "secret", time.Now()),
	})
	tool := NewProjectSessionHistoryTool(store)

	result, err := tool.Execute(context.WithValue(context.Background(), "session_id", "current"), mustJSON(t, map[string]any{
		"action":     "get",
		"session_id": "other",
	}))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected outside-project request to fail")
	}
	if !strings.Contains(result.Error, "outside the current project scope") {
		t.Fatalf("unexpected error: %s", result.Error)
	}
}

func TestProjectSessionHistoryToolGetReturnsTranscriptTail(t *testing.T) {
	projectID := "project-a"
	session := projectHistorySession("past", projectID, "Past", "completed", "Useful summary", time.Now())
	session.Messages = []storage.Message{
		{Role: "user", Content: "first", Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)},
		{Role: "assistant", Content: "second", Timestamp: time.Date(2026, 1, 1, 10, 1, 0, 0, time.UTC)},
		{Role: "user", Content: "third", Timestamp: time.Date(2026, 1, 1, 10, 2, 0, 0, time.UTC)},
	}
	store := newMockProjectSessionHistoryStore([]*storage.Session{
		projectHistorySession("current", projectID, "Current", "running", "", time.Now()),
		session,
	})
	tool := NewProjectSessionHistoryTool(store)

	result, err := tool.Execute(context.WithValue(context.Background(), "session_id", "current"), mustJSON(t, map[string]any{
		"action":       "get",
		"session_id":   "past",
		"max_messages": 2,
	}))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}
	if strings.Contains(result.Output, "first") {
		t.Fatalf("expected transcript to be tailed: %s", result.Output)
	}
	if !strings.Contains(result.Output, "second") || !strings.Contains(result.Output, "third") {
		t.Fatalf("expected last two messages in output: %s", result.Output)
	}
	if !strings.Contains(result.Output, "1 earlier omitted") {
		t.Fatalf("expected omitted count in output: %s", result.Output)
	}
}

func TestManagerWithStoreRegistersProjectSessionHistoryTool(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	manager := NewManagerWithStore(".", store)
	if _, ok := manager.Get("project_session_history"); !ok {
		t.Fatal("expected project_session_history to be registered")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return data
}

func projectHistorySession(id, projectID, title, status, summary string, updatedAt time.Time) *storage.Session {
	createdAt := updatedAt.Add(-time.Hour)
	return &storage.Session{
		ID:        id,
		AgentID:   "build",
		ProjectID: &projectID,
		Title:     title,
		Summary:   summary,
		Status:    status,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
}

type mockProjectSessionHistoryStore struct {
	sessions map[string]*storage.Session
}

func newMockProjectSessionHistoryStore(sessions []*storage.Session) *mockProjectSessionHistoryStore {
	store := &mockProjectSessionHistoryStore{sessions: make(map[string]*storage.Session)}
	for _, sess := range sessions {
		store.sessions[sess.ID] = sess
	}
	return store
}

func (m *mockProjectSessionHistoryStore) GetSession(id string) (*storage.Session, error) {
	if sess, ok := m.sessions[id]; ok {
		return sess, nil
	}
	return nil, fmt.Errorf("session not found: %s", id)
}

func (m *mockProjectSessionHistoryStore) GetSessionSummary(id string) (*storage.Session, error) {
	return m.GetSession(id)
}

func (m *mockProjectSessionHistoryStore) ListSessions() ([]*storage.Session, error) {
	items := make([]*storage.Session, 0, len(m.sessions))
	for _, sess := range m.sessions {
		items = append(items, sess)
	}
	return items, nil
}
