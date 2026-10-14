package http

import (
	"context"
	"strings"
	"testing"
)

func TestTelegramTranscriptionFollowsSpeechSettings(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	// The engine comes from the Speech settings (AAGENT_STT_ENGINE), not from a Telegram-specific choice.
	t.Setenv("AAGENT_STT_ENGINE", "definitely-not-an-engine")
	_, err := server.transcribeTelegramAudio(context.Background(), "/nonexistent/audio.wav")
	if err == nil || !strings.Contains(err.Error(), "definitely-not-an-engine") {
		t.Fatalf("expected the configured engine to be used, got %v", err)
	}
}

func TestTelegramReplyAudioFilenameMatchesContentType(t *testing.T) {
	cases := map[string]string{
		"audio/mpeg":               "reply.mp3",
		"audio/mpeg; charset=utf8": "reply.mp3",
		"audio/ogg":                "reply.ogg",
		"audio/mp4":                "reply.m4a",
		"audio/wav":                "reply.wav",
		"":                         "reply.wav",
	}
	for contentType, want := range cases {
		if got := telegramReplyAudioFilename(contentType); got != want {
			t.Errorf("%q: got %q, want %q", contentType, got, want)
		}
	}
}

func TestTelegramReplySpeechUsesConfiguredTTSEngine(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	t.Setenv("AAGENT_TTS_ENGINE", "no_such_engine:voice")
	_, err := server.synthesizeTelegramReplyAudio(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "unsupported speech engine") {
		t.Fatalf("expected the configured TTS engine to be consulted, got %v", err)
	}
}
