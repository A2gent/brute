package claudecli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestNewClientDefaultEnablesSessionPersistence(t *testing.T) {
	t.Setenv("AAGENT_CLAUDE_CLI_NO_SESSION_PERSISTENCE", "")
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "")

	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeClaude := writeFakeClaudeScript(t, tmp, argsFile, `{"type":"result","subtype":"success","result":"ok","session_id":"cursor-default","usage":{"input_tokens":1,"output_tokens":1}}`)

	client := NewClient("claude-sonnet-4-6", tmp)
	if _, err := client.Chat(t.Context(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
	}); err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	joined := readCapturedArgs(t, argsFile)
	if strings.Contains(joined, "--no-session-persistence") {
		t.Fatalf("default NewClient should enable session persistence, got args:\n%s", joined)
	}
	_ = fakeClaude
}

func TestClientChatFirstDurableRequestDoesNotForceSessionID(t *testing.T) {
	t.Setenv("AAGENT_CLAUDE_CLI_NO_SESSION_PERSISTENCE", "")
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "")

	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	writeFakeClaudeScript(t, tmp, argsFile, `{"type":"result","subtype":"success","result":"ok","session_id":"cursor-first","usage":{"input_tokens":1,"output_tokens":1}}`)

	client := NewClient("claude-sonnet-4-6", tmp)
	if _, err := client.Chat(t.Context(), &llm.ChatRequest{
		SessionID: "a2gent-session-uuid-0000-0000-0000-000000000001",
		Messages:  []llm.Message{{Role: "user", Content: "hello"}},
	}); err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	joined := readCapturedArgs(t, argsFile)
	if strings.Contains(joined, "--session-id") {
		t.Fatalf("first durable request must not force A2gent SessionID via --session-id, got:\n%s", joined)
	}
}

func TestClientChatWithProviderSessionCursorUsesResumeNotSessionID(t *testing.T) {
	t.Setenv("AAGENT_CLAUDE_CLI_NO_SESSION_PERSISTENCE", "")
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "")

	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	writeFakeClaudeScript(t, tmp, argsFile, `{"type":"result","subtype":"success","result":"ok","session_id":"cursor-resumed","usage":{"input_tokens":1,"output_tokens":1}}`)

	client := NewClient("claude-sonnet-4-6", tmp)
	if _, err := client.Chat(t.Context(), &llm.ChatRequest{
		ProviderSessionCursor: "018b187a-7b35-444c-8d6f-92b90e8d7b64",
		SessionID:             "a2gent-session-uuid-0000-0000-0000-000000000001",
		Messages: []llm.Message{
			{Role: "user", Content: "earlier"},
			{Role: "user", Content: "latest only"},
		},
	}); err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	joined := readCapturedArgs(t, argsFile)
	if !strings.Contains(joined, "--resume") || !strings.Contains(joined, "018b187a-7b35-444c-8d6f-92b90e8d7b64") {
		t.Fatalf("expected --resume <cursor>, got:\n%s", joined)
	}
	if strings.Contains(joined, "--session-id") {
		t.Fatalf("provider session resume must not use --session-id, got:\n%s", joined)
	}
}

func TestClientChatProviderSessionUsesDeltaPrompt(t *testing.T) {
	t.Setenv("AAGENT_CLAUDE_CLI_NO_SESSION_PERSISTENCE", "")
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "")

	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	writeFakeClaudeScript(t, tmp, argsFile, `{"type":"result","subtype":"success","result":"ok","session_id":"cursor-prompt","usage":{"input_tokens":1,"output_tokens":1}}`)

	client := NewClient("claude-sonnet-4-6", tmp)
	if _, err := client.Chat(t.Context(), &llm.ChatRequest{
		ProviderSessionCursor: "018b187a-7b35-444c-8d6f-92b90e8d7b64",
		Messages:              []llm.Message{{Role: "user", Content: "latest only"}},
	}); err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	args := strings.Split(strings.TrimSpace(readCapturedArgs(t, argsFile)), "\n")
	if len(args) < 2 || args[0] != "-p" {
		t.Fatalf("expected -p prompt flag first, got %#v", args)
	}
	if args[1] != "latest only" {
		t.Fatalf("prompt = %q, want delta message content", args[1])
	}
}

func TestClientChatReturnsProviderSessionCursor(t *testing.T) {
	t.Setenv("AAGENT_CLAUDE_CLI_NO_SESSION_PERSISTENCE", "")
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "")

	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	writeFakeClaudeScript(t, tmp, argsFile, `{"type":"result","subtype":"success","result":"done","session_id":"018b187a-7b35-444c-8d6f-92b90e8d7b64","usage":{"input_tokens":1,"output_tokens":1}}`)

	client := NewClient("claude-sonnet-4-6", tmp)
	resp, err := client.Chat(t.Context(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if resp.ProviderSessionCursor != "018b187a-7b35-444c-8d6f-92b90e8d7b64" {
		t.Fatalf("ProviderSessionCursor = %q, want provider session id from CLI", resp.ProviderSessionCursor)
	}
	_ = argsFile
}

func writeFakeClaudeScript(t *testing.T, tmp, argsFile, resultJSON string) string {
	t.Helper()
	fakeClaude := filepath.Join(tmp, "claude")
	script := "#!/bin/sh\n" +
		": > \"$ARGS_FILE\"\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"$ARGS_FILE\"; done\n" +
		"printf '%s\\n' '" + resultJSON + "'\n"
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake claude: %v", err)
	}
	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", fakeClaude)
	return fakeClaude
}

func readCapturedArgs(t *testing.T, argsFile string) string {
	t.Helper()
	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("failed to read captured args: %v", err)
	}
	return strings.Join(strings.Split(strings.TrimSpace(string(rawArgs)), "\n"), "\n")
}
