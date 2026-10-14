package http

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramTestTopicValidatesInput(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	cases := map[string]string{
		`{}`:                                   "chat_id is required",
		`{"chat_id":"-100"}`:                   "bot_token is required",
		`{"chat_id":"-100","bot_token":"***"}`: "bot_token is required",
	}
	for body, want := range cases {
		req := httptest.NewRequest(http.MethodPost, "/integrations/telegram/test-topic", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		server.handleTelegramTestTopic(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("body %s: got %d %s, want %q", body, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestTelegramTopicFixAdviceCoversTheCommonCases(t *testing.T) {
	cases := map[string]string{
		"telegram createForumTopic failed: Bad Request: not enough rights to create a topic": "Manage topics",
		"telegram createForumTopic failed: Bad Request: the chat is not a forum":             "turn on Topics",
		"telegram createForumTopic failed: Bad Request: chat not found":                      "not in this group",
		"telegram createForumTopic failed: boom":                                             "Reason:",
	}
	for in, want := range cases {
		if got := telegramTopicFixAdvice(errors.New(in)); !strings.Contains(got, want) {
			t.Errorf("%q: advice %q does not mention %q", in, got, want)
		}
	}
}
