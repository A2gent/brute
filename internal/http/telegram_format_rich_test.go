package http

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitTelegramTextPrefersParagraphBoundaries(t *testing.T) {
	para := strings.Repeat("слово ", 20) // 120 runes
	text := strings.TrimSpace(para + "\n\n" + para + "\n\n" + para)
	parts := splitTelegramText(text, 300)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %q", len(parts), parts)
	}
	for _, part := range parts {
		if utf8.RuneCountInString(part) > 300 {
			t.Fatalf("part longer than limit: %d", utf8.RuneCountInString(part))
		}
		if strings.HasPrefix(part, "ово") || strings.HasSuffix(part, "сло") {
			t.Fatalf("word was cut in the middle: %q", part)
		}
	}
	if strings.Join(strings.Fields(strings.Join(parts, " ")), " ") != strings.Join(strings.Fields(text), " ") {
		t.Fatal("splitting lost or changed text")
	}
}

func TestSplitTelegramTextHardCutsWhenThereIsNoBreak(t *testing.T) {
	parts := splitTelegramText(strings.Repeat("x", 250), 100)
	if len(parts) != 3 || utf8.RuneCountInString(parts[0]) != 100 || utf8.RuneCountInString(parts[2]) != 50 {
		t.Fatalf("unexpected parts: %d %v", len(parts), []int{len(parts[0]), len(parts[len(parts)-1])})
	}
}

func TestTelegramAgentPromptContextAsksForPhoneFriendlyStyle(t *testing.T) {
	got := telegramAgentPromptContext("привет", map[string]interface{}{"inbound_message_type": "voice"})
	for _, want := range []string{"Inbound channel: Telegram", "message type: voice", "phone chat", "no tables or headings", "\nпривет"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt context missing %q:\n%s", want, got)
		}
	}
	if telegramAgentPromptContext("  ", nil) != "" {
		t.Fatal("empty message must stay empty")
	}
}
