package http

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramTopicFailureHintExplainsTheFix(t *testing.T) {
	cases := map[string]string{
		"telegram createForumTopic failed: Bad Request: not enough rights to create a topic": "Manage topics",
		"telegram createForumTopic failed: Bad Request: the chat is not a forum":             "turn on Topics",
		"telegram createForumTopic failed: something else":                                   "Reason:",
	}
	for in, want := range cases {
		got := telegramTopicFailureHint(errors.New(in))
		if !strings.Contains(got, want) {
			t.Errorf("%q: hint %q does not mention %q", in, got, want)
		}
	}
}

func TestTelegramTopicWarningIsThrottledPerChat(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	chat := "-100-throttle-test"
	telegramTopicWarnings.Delete(chat)
	if !server.shouldWarnTelegramTopic(chat) {
		t.Fatal("the first failure must warn")
	}
	if server.shouldWarnTelegramTopic(chat) {
		t.Fatal("a second warning right after must be suppressed")
	}
	if !server.shouldWarnTelegramTopic("-100-other-chat") {
		t.Fatal("other chats are throttled separately")
	}
}

func TestTelegramCheckGroupsRequiresToken(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/integrations/telegram/check-groups", bytes.NewBufferString(`{"bot_token":"***"}`))
	rec := httptest.NewRecorder()
	server.handleTelegramCheckGroups(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bot_token is required") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
