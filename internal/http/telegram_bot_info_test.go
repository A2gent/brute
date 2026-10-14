package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramBotInfoRequiresToken(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	for _, body := range []string{`{}`, `{"bot_token":"***"}`, `{"integration_id":"missing"}`} {
		req := httptest.NewRequest(http.MethodPost, "/integrations/telegram/bot-info", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		server.handleTelegramBotInfo(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bot_token is required") {
			t.Fatalf("body %s: got %d %s", body, rec.Code, rec.Body.String())
		}
	}
}
