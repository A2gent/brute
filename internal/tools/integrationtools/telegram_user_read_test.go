package integrationtools

import (
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestTelegramUserAllowedChats(t *testing.T) {
	in := &storage.Integration{Config: map[string]string{"allowed_chats": "123, -100456\n789"}}
	allowed := TelegramUserAllowedChats(in)
	for _, id := range []string{"123", "-100456", "789"} {
		if _, ok := allowed[id]; !ok {
			t.Fatalf("expected %s to be allowed", id)
		}
	}
	if _, ok := allowed["999"]; ok {
		t.Fatal("unexpected chat allowed")
	}
	if got := TelegramUserAllowedChats(&storage.Integration{Config: map[string]string{}}); len(got) != 0 {
		t.Fatal("empty allowlist must allow nothing")
	}
}

func TestTelegramUserSessionRoundTrip(t *testing.T) {
	in := &storage.Integration{Config: map[string]string{"api_id": "42", "api_hash": "h"}}
	TelegramUserSetSession(in, []byte("sess"))
	creds, err := TelegramUserCredentials(in)
	if err != nil || creds.APIID != 42 || string(creds.Session) != "sess" {
		t.Fatalf("round trip failed: %+v %v", creds, err)
	}
}
