package http

import (
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestResolveTelegramDiscoveryToken(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	for _, integration := range []*storage.Integration{
		{ID: "tg-1", Provider: "telegram", Name: "Bot", Mode: "duplex", Enabled: true, Config: map[string]string{"bot_token": "stored-token"}},
		{ID: "slack-1", Provider: "slack", Name: "Slack", Mode: "notify_only", Enabled: true, Config: map[string]string{"bot_token": "slack-token"}},
	} {
		if err := store.SaveIntegration(integration); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name string
		req  TelegramChatDiscoveryRequest
		want string
	}{
		{"typed token wins", TelegramChatDiscoveryRequest{BotToken: "typed", IntegrationID: "tg-1"}, "typed"},
		{"masked token resolves stored one", TelegramChatDiscoveryRequest{BotToken: "***", IntegrationID: "tg-1"}, "stored-token"},
		{"empty token resolves stored one", TelegramChatDiscoveryRequest{IntegrationID: "tg-1"}, "stored-token"},
		{"masked token without integration stays masked", TelegramChatDiscoveryRequest{BotToken: "***"}, "***"},
		{"unknown integration", TelegramChatDiscoveryRequest{IntegrationID: "nope"}, ""},
		{"other providers are never used", TelegramChatDiscoveryRequest{IntegrationID: "slack-1"}, ""},
	}
	for _, tc := range cases {
		if got := server.resolveTelegramDiscoveryToken(tc.req); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRecordSeenTelegramChat(t *testing.T) {
	integration := &storage.Integration{Config: map[string]string{}}
	private := telegramChatPayload{ID: 42, Type: "private", FirstName: "Art"}
	group := telegramChatPayload{ID: -100123, Type: "supergroup", Title: "Class 2B"}

	if !recordSeenTelegramChat(integration, private) || !recordSeenTelegramChat(integration, group) {
		t.Fatal("new chats must be recorded")
	}
	if recordSeenTelegramChat(integration, private) {
		t.Fatal("an unchanged chat must not trigger a save")
	}
	group.Title = "Class 2B (renamed)"
	if !recordSeenTelegramChat(integration, group) {
		t.Fatal("a renamed chat must be updated")
	}
	seen := telegramSeenChats(integration)
	if len(seen) != 2 || seen[1].Title != "Class 2B (renamed)" {
		t.Fatalf("seen = %#v", seen)
	}
	if recordSeenTelegramChat(integration, telegramChatPayload{}) {
		t.Fatal("chat id 0 must be ignored")
	}
}

func TestMergeTelegramChatCandidatesPrefersFirstAndSorts(t *testing.T) {
	merged := mergeTelegramChatCandidates(
		[]TelegramChatCandidate{{ChatID: "2", Title: "seen"}, {ChatID: "1"}},
		[]TelegramChatCandidate{{ChatID: "2", Title: "fresh"}, {ChatID: "3"}},
	)
	if len(merged) != 3 || merged[0].ChatID != "1" || merged[1].Title != "seen" || merged[2].ChatID != "3" {
		t.Fatalf("merged = %#v", merged)
	}
}
