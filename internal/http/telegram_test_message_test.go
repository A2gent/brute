package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramTestMessageValidatesInput(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	cases := map[string]string{
		`{"chat_id":""}`:                     "chat_id is required",
		`{"chat_id":"42"}`:                   "bot_token is required",
		`{"chat_id":"42","bot_token":"***"}`: "bot_token is required",
	}
	for body, want := range cases {
		req := httptest.NewRequest(http.MethodPost, "/integrations/telegram/test-message", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		server.handleTelegramTestMessage(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("body %s: got %d %s, want %q", body, rec.Code, rec.Body.String(), want)
		}
	}
}
