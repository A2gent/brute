package http

import "testing"

func TestDetectSpeechLanguage(t *testing.T) {
	if got := detectSpeechLanguage("Привет, мир"); got != "ru" {
		t.Fatalf("got %q", got)
	}
	if got := detectSpeechLanguage("what ADRs are waiting"); got != "en" {
		t.Fatalf("got %q", got)
	}
}
