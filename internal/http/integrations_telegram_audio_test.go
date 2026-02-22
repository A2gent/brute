package http

import (
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestTelegramAudioFileIDForMessage(t *testing.T) {
	tests := []struct {
		name    string
		message *telegramMessagePayload
		wantID  string
		wantTyp string
	}{
		{
			name: "voice preferred",
			message: &telegramMessagePayload{
				Voice: &telegramVoicePayload{FileID: "voice-id"},
			},
			wantID:  "voice-id",
			wantTyp: "voice",
		},
		{
			name: "audio fallback",
			message: &telegramMessagePayload{
				Audio: &telegramAudioPayload{FileID: "audio-id"},
			},
			wantID:  "audio-id",
			wantTyp: "audio",
		},
		{
			name: "document with audio mime",
			message: &telegramMessagePayload{
				Document: &telegramAudioPayload{FileID: "doc-id", MimeType: "audio/ogg"},
			},
			wantID:  "doc-id",
			wantTyp: "audio document",
		},
		{
			name: "document non audio ignored",
			message: &telegramMessagePayload{
				Document: &telegramAudioPayload{FileID: "doc-id", MimeType: "application/pdf"},
			},
			wantID:  "",
			wantTyp: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotID, gotTyp := telegramAudioFileIDForMessage(tc.message)
			if gotID != tc.wantID || gotTyp != tc.wantTyp {
				t.Fatalf("telegramAudioFileIDForMessage() = (%q, %q), want (%q, %q)", gotID, gotTyp, tc.wantID, tc.wantTyp)
			}
		})
	}
}

func TestTelegramVoiceTranscriptionEnabled(t *testing.T) {
	tests := []struct {
		name        string
		integration *storage.Integration
		want        bool
	}{
		{
			name: "default enabled when missing config",
			integration: &storage.Integration{
				Config: map[string]string{},
			},
			want: true,
		},
		{
			name: "enabled true",
			integration: &storage.Integration{
				Config: map[string]string{"transcribe_voice_messages": "true"},
			},
			want: true,
		},
		{
			name: "disabled false",
			integration: &storage.Integration{
				Config: map[string]string{"transcribe_voice_messages": "false"},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := telegramVoiceTranscriptionEnabled(tc.integration)
			if got != tc.want {
				t.Fatalf("telegramVoiceTranscriptionEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTelegramPromptFromInboundMessage_TextAndCaptionWithoutMedia(t *testing.T) {
	s := &Server{}

	textPrompt, err := s.telegramPromptFromInboundMessage(
		t.Context(),
		"token",
		&storage.Integration{Config: map[string]string{}},
		&telegramMessagePayload{Text: "hello from text"},
	)
	if err != nil {
		t.Fatalf("unexpected error for text message: %v", err)
	}
	if textPrompt != "hello from text" {
		t.Fatalf("expected text prompt, got %q", textPrompt)
	}

	captionPrompt, err := s.telegramPromptFromInboundMessage(
		t.Context(),
		"token",
		&storage.Integration{Config: map[string]string{"transcribe_voice_messages": "false"}},
		&telegramMessagePayload{Caption: "caption only"},
	)
	if err != nil {
		t.Fatalf("unexpected error for caption-only message: %v", err)
	}
	if captionPrompt != "caption only" {
		t.Fatalf("expected caption prompt, got %q", captionPrompt)
	}
}
