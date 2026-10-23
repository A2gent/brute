package cursorcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

const testChatID = "11111111-2222-3333-4444-555555555555"

func TestBuildArgsAddsResumeOnlyWithChatID(t *testing.T) {
	client := NewClientWithOptions("composer-2.5", Options{WorkDir: t.TempDir(), APIKey: "k"})

	fresh := client.buildArgs("composer-2.5", "hello", "", mcpBridgeInvocation{})
	if strings.Contains(strings.Join(fresh, "\n"), "--resume") {
		t.Fatalf("fresh chat must not pass --resume: %#v", fresh)
	}
	resumed := client.buildStreamArgs("composer-2.5", "hello", testChatID, mcpBridgeInvocation{})
	if !containsPair(resumed, "--resume", testChatID) {
		t.Fatalf("resumed chat must pass --resume %s: %#v", testChatID, resumed)
	}
}

func TestResumeChatIDRequiresMatchingScopeAndLocalChat(t *testing.T) {
	chats := useTempChatsDir(t)
	work := t.TempDir()
	client := NewClientWithOptions("composer-2.5", Options{WorkDir: work, APIKey: "k"})
	stored := "cursor:abc|" + client.sessionCursor("composer-2.5", testChatID)

	if got := client.resumeChatID("composer-2.5", stored); got != "" {
		t.Fatalf("chat missing locally must not resume, got %q", got)
	}
	createChat(t, chats, testChatID)
	if got := client.resumeChatID("composer-2.5", stored); got != testChatID {
		t.Fatalf("resumeChatID = %q, want %q", got, testChatID)
	}
	if got := client.resumeChatID("gpt-5", stored); got != "" {
		t.Fatalf("model change must start a fresh chat, got %q", got)
	}
	other := NewClientWithOptions("composer-2.5", Options{WorkDir: t.TempDir(), APIKey: "k"})
	if got := other.resumeChatID("composer-2.5", stored); got != "" {
		t.Fatalf("workspace change must start a fresh chat, got %q", got)
	}
	otherKey := NewClientWithOptions("composer-2.5", Options{WorkDir: work, APIKey: "k2"})
	if got := otherKey.resumeChatID("composer-2.5", stored); got != "" {
		t.Fatalf("API key change must start a fresh chat, got %q", got)
	}
}

func TestChatResumeSendsOnlyNewMessages(t *testing.T) {
	chats := useTempChatsDir(t)
	createChat(t, chats, testChatID)
	tmp := t.TempDir()
	client, argsFile := newFakeCursorClient(t, tmp, false)

	resp, err := client.Chat(t.Context(), resumeRequest(client))
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	calls := readCalls(t, argsFile)
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if !containsPair(calls[0], "--resume", testChatID) {
		t.Fatalf("expected --resume %s: %#v", testChatID, calls[0])
	}
	if prompt := promptArg(calls[0]); prompt != "second question" {
		t.Fatalf("resume prompt = %q, want only the new message", prompt)
	}
	want := client.sessionCursor("composer-2.5", "99999999-0000-0000-0000-000000000001")
	if resp.ProviderSessionCursor != want {
		t.Fatalf("ProviderSessionCursor = %q, want %q", resp.ProviderSessionCursor, want)
	}
}

func TestChatStreamFallsBackToFreshChatWhenResumeFails(t *testing.T) {
	chats := useTempChatsDir(t)
	createChat(t, chats, testChatID)
	tmp := t.TempDir()
	client, argsFile := newFakeCursorClient(t, tmp, true)

	resp, err := client.ChatStream(t.Context(), resumeRequest(client), nil)
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	calls := readCalls(t, argsFile)
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want resume attempt + fresh retry", len(calls))
	}
	if containsPair(calls[1], "--resume", testChatID) {
		t.Fatalf("retry must not resume: %#v", calls[1])
	}
	prompt := promptArg(calls[1])
	for _, want := range []string{"Be brief.", "first question", "first answer", "second question"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("fallback prompt missing %q: %s", want, prompt)
		}
	}
	want := client.sessionCursor("composer-2.5", "99999999-0000-0000-0000-000000000002")
	if resp.ProviderSessionCursor != want {
		t.Fatalf("ProviderSessionCursor = %q, want new chat cursor %q", resp.ProviderSessionCursor, want)
	}
}

func resumeRequest(client *Client) *llm.ChatRequest {
	full := []llm.Message{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
	}
	return &llm.ChatRequest{
		SystemPrompt:          "Be brief.",
		Messages:              full[2:],
		ProviderSessionCursor: "cursor:abc|" + client.sessionCursor("composer-2.5", testChatID),
		FullMessages:          func() []llm.Message { return full },
	}
}

// newFakeCursorClient records each call's args (NUL-separated, RS-terminated) and
// answers with a new chat ID per call. failResume rejects any --resume call.
func newFakeCursorClient(t *testing.T, tmp string, failResume bool) (*Client, string) {
	t.Helper()
	argsFile := filepath.Join(tmp, "args.txt")
	callFile := filepath.Join(tmp, "calls.txt")
	script := `#!/bin/sh
call=1
if [ -f "$CALL_FILE" ]; then call=$(($(cat "$CALL_FILE") + 1)); fi
printf '%s' "$call" > "$CALL_FILE"
printf '%s\0' "$@" >> "$ARGS_FILE"
printf '\036' >> "$ARGS_FILE"
for arg in "$@"; do
  if [ "$arg" = "--resume" ] && [ -n "$FAIL_RESUME" ]; then echo "Failed to resume chat" >&2; exit 1; fi
done
printf '{"type":"result","subtype":"success","result":"answer","session_id":"99999999-0000-0000-0000-00000000000%s","usage":{"inputTokens":1,"outputTokens":1}}\n' "$call"
`
	fake := filepath.Join(tmp, "agent")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake agent: %v", err)
	}
	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("CALL_FILE", callFile)
	t.Setenv("AAGENT_CURSOR_CLI_PATH", "")
	if failResume {
		t.Setenv("FAIL_RESUME", "1")
	} else {
		t.Setenv("FAIL_RESUME", "")
	}
	return NewClientWithOptions("composer-2.5", Options{Executable: fake, WorkDir: tmp, APIKey: "k"}), argsFile
}

func useTempChatsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := cursorChatsDir
	cursorChatsDir = func() string { return dir }
	t.Cleanup(func() { cursorChatsDir = prev })
	return dir
}

func createChat(t *testing.T, chatsDir, chatID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(chatsDir, "workspacehash", chatID), 0o755); err != nil {
		t.Fatalf("failed to create chat dir: %v", err)
	}
}

func readCalls(t *testing.T, argsFile string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("failed to read args: %v", err)
	}
	var calls [][]string
	for _, frame := range strings.Split(strings.TrimSuffix(string(raw), "\x1e"), "\x1e") {
		if frame != "" {
			calls = append(calls, strings.Split(strings.TrimSuffix(frame, "\x00"), "\x00"))
		}
	}
	return calls
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func promptArg(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-p" {
			return args[i+1]
		}
	}
	return ""
}
