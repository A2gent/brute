package kimicli

import (
	"strings"
	"testing"
)

func TestNormalizeKimiCLIErrorMessageAddsTargetedHints(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw  string
		want string
	}{
		{
			raw:  "429 too many requests",
			want: "rate limit",
		},
		{
			raw:  "authentication required",
			want: "kimi login",
		},
		{
			raw:  "No model configured. Run `kimi` and use /login",
			want: "config.toml",
		},
	}

	for _, tc := range cases {
		got := normalizeKimiCLIErrorMessage(tc.raw)
		if !strings.Contains(strings.ToLower(got), strings.ToLower(tc.want)) {
			t.Fatalf("normalizeKimiCLIErrorMessage(%q) = %q, want hint containing %q", tc.raw, got, tc.want)
		}
	}
}

func TestMessageTextSupportsStringAndArrayContent(t *testing.T) {
	t.Parallel()

	if got := messageText([]byte(`"hello"`)); got != "hello" {
		t.Fatalf("messageText(string) = %q, want hello", got)
	}
	if got := messageText([]byte(`[{"type":"text","text":"hi"}]`)); got != "hi" {
		t.Fatalf("messageText(array) = %q, want hi", got)
	}
}

func TestIsKimiSessionID(t *testing.T) {
	t.Parallel()

	if !isKimiSessionID("session_5683e072-9217-4208-90b8-7d66b3d12f51") {
		t.Fatal("expected kimi session id to be recognized")
	}
	if isKimiSessionID("not-a-session") {
		t.Fatal("expected non-session id to be rejected")
	}
}

func TestFallbackModelsAreNonEmpty(t *testing.T) {
	t.Parallel()

	models := fallbackModels()
	if len(models) == 0 {
		t.Fatal("fallbackModels() returned empty list")
	}
}
