package kimicli

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestBuildArgsUsesPromptModeWithoutDeprecatedPrintFlag(t *testing.T) {
	t.Parallel()

	client := NewClientWithOptions("kimi-code/kimi-for-coding", Options{WorkDir: t.TempDir(), Yolo: true})
	args := strings.Join(client.buildArgs(&llm.ChatRequest{}, "kimi-code/kimi-for-coding", "hello"), "\n")

	for _, forbidden := range []string{"--print", "--yolo"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("expected args to omit %q: %s", forbidden, args)
		}
	}
	for _, required := range []string{"-p", "hello", "--output-format", "stream-json", "-m", "kimi-code/kimi-for-coding"} {
		if !strings.Contains(args, required) {
			t.Fatalf("expected args to include %q: %s", required, args)
		}
	}
}

func TestBuildArgsResumesKimiSession(t *testing.T) {
	t.Parallel()

	client := NewClientWithOptions("kimi-code/kimi-for-coding", Options{WorkDir: t.TempDir()})
	args := client.buildArgs(
		&llm.ChatRequest{SessionID: "session_5683e072-9217-4208-90b8-7d66b3d12f51"},
		"kimi-code/kimi-for-coding",
		"hello",
	)

	if len(args) < 2 || args[len(args)-2] != "-S" || args[len(args)-1] != "session_5683e072-9217-4208-90b8-7d66b3d12f51" {
		t.Fatalf("expected session resume args, got %v", args)
	}
}

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
