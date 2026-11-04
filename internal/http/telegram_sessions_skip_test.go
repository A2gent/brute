package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestSyncHTTPCreatedSessionToTelegramHonorsSkipFlag(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	// Chat scope binds the session before any Telegram request, so a missed skip shows up in the metadata.
	integration := &storage.Integration{ID: "tg-1", Provider: "telegram", Name: "Bot", Mode: "duplex", Enabled: true,
		Config: map[string]string{"bot_token": "token", "default_chat_id": "42", "session_scope": "chat"}}
	if err := store.SaveIntegration(integration); err != nil {
		t.Fatal(err)
	}
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.Metadata = map[string]interface{}{sessionSkipTelegramSyncKey: true}
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}

	server.syncHTTPCreatedSessionToTelegram(context.Background(), sess.ID, "private conversation")

	got, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if chatID := metadataString(got.Metadata["telegram_chat_id"]); chatID != "" {
		t.Fatalf("session was bound to Telegram chat %q", chatID)
	}
}

func TestCreateSessionInheritsTelegramSkipAndDropsSessionOfUnknownAgent(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	post := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		server.handleCreateSession(recorder, httptest.NewRequest(nethttp.MethodPost, "/sessions", strings.NewReader(body)))
		return recorder
	}

	parent := post(`{"agent_id":"build","metadata":{"skip_telegram_sync":true}}`)
	var created CreateSessionResponse
	if err := json.Unmarshal(parent.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	child := post(`{"agent_id":"build","parent_id":"` + created.ID + `"}`)
	if err := json.Unmarshal(child.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	sess, err := server.sessionManager.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sessionSkipsTelegramSync(sess) {
		t.Fatal("child session must inherit skip_telegram_sync")
	}

	before, err := server.sessionManager.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := post(`{"agent_id":"build","unified_agent_id":"missing","task":"private conversation"}`); got.Code != nethttp.StatusBadRequest {
		t.Fatalf("status = %d", got.Code)
	}
	after, err := server.sessionManager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("rejected session stayed behind: %d -> %d sessions", len(before), len(after))
	}
}
