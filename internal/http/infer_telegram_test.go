package http

import (
	"os"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestInferTelegramChatIDForIntegration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "telegram_infer_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := storage.NewSQLiteStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create SQLite store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	srv := &Server{
		sessionManager: sm,
	}

	integrationID := "int-1"

	// Create an old DM session without thread id
	sessDM, _ := sm.Create("agent")
	sessDM.Metadata = map[string]interface{}{
		"integration_provider": "telegram",
		"integration_id":   integrationID,
		"telegram_chat_id": "dm-chat-id",
	}
	sessDM.UpdatedAt = time.Now().Add(-2 * time.Hour)
	sm.Save(sessDM)

	// Create a newer Topic session with thread id
	sessTopic, _ := sm.Create("agent")
	sessTopic.Metadata = map[string]interface{}{
		"integration_provider": "telegram",
		"integration_id":     integrationID,
		"telegram_chat_id":   "group-chat-id",
		"telegram_thread_id": "42",
	}
	sessTopic.UpdatedAt = time.Now().Add(-1 * time.Hour)
	sm.Save(sessTopic)

	// In "chat" scope, the newest session (sessTopic) should win
	chatIDErr1 := srv.inferTelegramChatIDForIntegration(integrationID, "chat")
	if chatIDErr1 != "group-chat-id" {
		t.Errorf("expected group-chat-id for chat scope, got: %s", chatIDErr1)
	}

	// Update DM session to be the newest
	sessDM.UpdatedAt = time.Now()
	sm.Save(sessDM)

	// In "chat" scope, DM session should win
	chatIDErr2 := srv.inferTelegramChatIDForIntegration(integrationID, "chat")
	if chatIDErr2 != "dm-chat-id" {
		t.Errorf("expected dm-chat-id for chat scope after update, got: %s", chatIDErr2)
	}

	// In "topic" scope, sessTopic should win because sessDM has no thread_id
	chatIDErr3 := srv.inferTelegramChatIDForIntegration(integrationID, "topic")
	if chatIDErr3 != "group-chat-id" {
		t.Errorf("expected group-chat-id for topic scope, got: %s", chatIDErr3)
	}
}