package cursorcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestBuildPromptIncludesConversationToolContext(t *testing.T) {
	prompt := buildPrompt(&llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "Check the branch."},
			{
				Role:    "assistant",
				Content: "I'll inspect it.",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "bash", Input: `{"command":"git status"}`},
				},
			},
			{
				Role: "tool",
				ToolResults: []llm.ToolResult{
					{ToolCallID: "call_1", Name: "bash", Content: "clean"},
				},
			},
			{Role: "user", Content: "Summarize."},
		},
	})

	for _, want := range []string{
		"Continue the following A2gent conversation",
		"User:\nCheck the branch.",
		"[Tool call: bash id=call_1]",
		`{"command":"git status"}`,
		"[Tool result: bash id=call_1]",
		"User:\nSummarize.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestParseCLIResultAndUsage(t *testing.T) {
	raw := `{"type":"result","subtype":"success","result":"done","session_id":"018b187a-7b35-444c-8d6f-92b90e8d7b64","usage":{"inputTokens":7,"outputTokens":3,"cacheReadTokens":2}}`
	parsed, _, err := parseCLIResult(raw)
	if err != nil {
		t.Fatalf("parseCLIResult returned error: %v", err)
	}
	if parsed.Result != "done" {
		t.Fatalf("unexpected result: %q", parsed.Result)
	}
	usage := usageFromRaw(parsed.Usage)
	if usage.InputTokens != 7 || usage.OutputTokens != 3 || usage.CachedInputTokens != 2 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestClientArgsForceCommandsByDefault(t *testing.T) {
	t.Setenv("AAGENT_CURSOR_CLI_FORCE", "")
	t.Setenv("AAGENT_CURSOR_CLI_TRUST", "")

	client := NewClientWithOptions("composer-2.5", Options{WorkDir: t.TempDir()})
	args := strings.Join(client.buildArgs("composer-2.5", "hello"), "\n")

	if !strings.Contains(args, "--force") {
		t.Fatalf("expected default args to include --force: %s", args)
	}
	if strings.Contains(args, "--trust") {
		t.Fatalf("expected --force to replace --trust by default: %s", args)
	}
}

func TestClientArgsCanDisableForceAndTrustWorkspaceViaEnv(t *testing.T) {
	t.Setenv("AAGENT_CURSOR_CLI_FORCE", "false")
	t.Setenv("AAGENT_CURSOR_CLI_TRUST", "")

	client := NewClientWithOptions("composer-2.5", Options{WorkDir: t.TempDir()})
	args := strings.Join(client.buildArgs("composer-2.5", "hello"), "\n")

	if strings.Contains(args, "--force") {
		t.Fatalf("expected AAGENT_CURSOR_CLI_FORCE=false to omit --force: %s", args)
	}
	if !strings.Contains(args, "--trust") {
		t.Fatalf("expected args to include --trust when force is disabled: %s", args)
	}
}

func TestClientArgsCanDisableWorkspaceTrustViaEnv(t *testing.T) {
	t.Setenv("AAGENT_CURSOR_CLI_FORCE", "false")
	t.Setenv("AAGENT_CURSOR_CLI_TRUST", "false")

	client := NewClientWithOptions("composer-2.5", Options{WorkDir: t.TempDir()})
	args := strings.Join(client.buildArgs("composer-2.5", "hello"), "\n")

	if strings.Contains(args, "--trust") {
		t.Fatalf("expected AAGENT_CURSOR_CLI_TRUST=false to omit --trust: %s", args)
	}
}

func TestClientChatInvokesCursorAgentExecutable(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeAgent := filepath.Join(tmp, "agent")
	script := "#!/bin/sh\n" +
		": > \"$ARGS_FILE\"\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"$ARGS_FILE\"; done\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\",\"usage\":{\"inputTokens\":1,\"outputTokens\":1}}'\n"
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake agent: %v", err)
	}

	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("AAGENT_CURSOR_CLI_PATH", "")
	t.Setenv("AAGENT_CURSOR_CLI_FORCE", "true")
	client := NewClientWithOptions("cursor/composer-2.5", Options{
		Executable: fakeAgent,
		WorkDir:    tmp,
		Force:      true,
		Sandbox:    "enabled",
	})
	resp, err := client.Chat(t.Context(), &llm.ChatRequest{
		SystemPrompt: "Be brief.",
		Messages: []llm.Message{
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("unexpected content: %q", resp.Content)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("failed to read captured args: %v", err)
	}
	joined := strings.Join(strings.Split(strings.TrimSpace(string(rawArgs)), "\n"), "\n")
	for _, want := range []string{
		"-p",
		"hello",
		"--output-format",
		"json",
		"--model",
		"composer-2.5",
		"--workspace",
		tmp,
		"--force",
		"--sandbox",
		"enabled",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("captured args missing %q: %s", want, joined)
		}
	}
}

func TestClientDoesNotPutAPIKeyInArgs(t *testing.T) {
	client := NewClientWithOptions("composer-2.5", Options{
		WorkDir: t.TempDir(),
		APIKey:  "cursor-secret-token",
	})

	args := strings.Join(client.buildArgs("composer-2.5", "hello"), "\n")
	if strings.Contains(args, "cursor-secret-token") || strings.Contains(args, "--api-key") {
		t.Fatalf("expected API key to be passed through environment only, got args: %s", args)
	}
}

func TestClientChatStreamInvokesCursorStreamJSON(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeAgent := filepath.Join(tmp, "agent")
	script := "#!/bin/sh\n" +
		": > \"$ARGS_FILE\"\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"$ARGS_FILE\"; done\n" +
		"printf '%s\\n' '{\"type\":\"assistant\",\"timestamp_ms\":1,\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"hel\"}]},\"session_id\":\"018b187a-7b35-444c-8d6f-92b90e8d7b64\"}'\n" +
		"printf '%s\\n' '{\"type\":\"assistant\",\"timestamp_ms\":2,\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"lo\"}]},\"session_id\":\"018b187a-7b35-444c-8d6f-92b90e8d7b64\"}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"hello\",\"session_id\":\"018b187a-7b35-444c-8d6f-92b90e8d7b64\",\"usage\":{\"inputTokens\":2,\"outputTokens\":1}}'\n"
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake agent: %v", err)
	}

	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("AAGENT_CURSOR_CLI_PATH", "")
	client := NewClientWithOptions("composer-2.5", Options{
		Executable: fakeAgent,
		WorkDir:    tmp,
	})

	var deltas []string
	resp, err := client.ChatStream(t.Context(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
	}, func(event llm.StreamEvent) error {
		if event.Type == llm.StreamEventContentDelta {
			deltas = append(deltas, event.ContentDelta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("unexpected content: %q", resp.Content)
	}
	if strings.Join(deltas, "") != "hello" {
		t.Fatalf("unexpected deltas: %#v", deltas)
	}
	if resp.Usage.InputTokens != 2 || resp.Usage.OutputTokens != 1 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("failed to read captured args: %v", err)
	}
	joined := strings.Join(strings.Split(strings.TrimSpace(string(rawArgs)), "\n"), "\n")
	for _, want := range []string{
		"--output-format",
		"stream-json",
		"--stream-partial-output",
		"--workspace",
		"--force",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("captured args missing %q: %s", want, joined)
		}
	}
}

func TestCLIErrorMessageExtractsStreamJSONResult(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}`,
		`{"type":"result","subtype":"error","is_error":true,"error":"Rate limit exceeded. Retry later."}`,
	}, "\n")

	got := cliErrorMessage(os.ErrPermission, stdout, "")
	if got != "Rate limit exceeded. Retry later." {
		t.Fatalf("unexpected cli error message: %q", got)
	}
}
