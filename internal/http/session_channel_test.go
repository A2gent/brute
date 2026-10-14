package http

import (
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestSessionChannelFromMetadata(t *testing.T) {
	cases := []struct {
		name        string
		metadata    map[string]interface{}
		wantChannel string
		wantDetail  string
	}{
		{"no metadata", nil, "", ""},
		{"web session", map[string]interface{}{"provider": "anthropic"}, "", ""},
		{"telegram without topic", map[string]interface{}{"integration_provider": "telegram"}, "telegram", ""},
		{"telegram topic", map[string]interface{}{"integration_provider": " Telegram ", "telegram_topic_name": " Homework "}, "telegram", "Homework"},
		{"other integration has no detail", map[string]interface{}{"integration_provider": "slack", "telegram_topic_name": "x"}, "slack", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			channel, detail := sessionChannel(&session.Session{Metadata: tc.metadata})
			if channel != tc.wantChannel || detail != tc.wantDetail {
				t.Fatalf("got (%q, %q), want (%q, %q)", channel, detail, tc.wantChannel, tc.wantDetail)
			}
		})
	}
	if channel, detail := sessionChannel(nil); channel != "" || detail != "" {
		t.Fatal("nil session must have no channel")
	}
}
