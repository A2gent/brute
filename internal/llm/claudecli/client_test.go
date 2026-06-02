package claudecli

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
	raw := `{"type":"result","subtype":"success","result":"done","session_id":"018b187a-7b35-444c-8d6f-92b90e8d7b64","usage":{"input_tokens":7,"output_tokens":3,"cache_read_input_tokens":2}}`
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

func TestClientChatInvokesClaudeExecutable(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeClaude := filepath.Join(tmp, "claude")
	script := "#!/bin/sh\n" +
		": > \"$ARGS_FILE\"\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"$ARGS_FILE\"; done\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}'\n"
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake claude: %v", err)
	}

	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "")
	client := NewClientWithOptions("anthropic/claude-sonnet-4-6", Options{
		Executable:           fakeClaude,
		WorkDir:              tmp,
		NoSessionPersistence: true,
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

	var args []string
	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("failed to read captured args: %v", err)
	}
	args = strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"-p",
		"hello",
		"--output-format",
		"json",
		"--model",
		"claude-sonnet-4-6",
		"--append-system-prompt",
		"Be brief.",
		"--no-session-persistence",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("captured args missing %q: %#v", want, args)
		}
	}
}

func TestClientChatStreamInvokesClaudeStreamJSON(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeClaude := filepath.Join(tmp, "claude")
	script := "#!/bin/sh\n" +
		": > \"$ARGS_FILE\"\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"$ARGS_FILE\"; done\n" +
		"printf '%s\\n' '{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hel\"}},\"session_id\":\"018b187a-7b35-444c-8d6f-92b90e8d7b64\"}'\n" +
		"printf '%s\\n' '{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}},\"session_id\":\"018b187a-7b35-444c-8d6f-92b90e8d7b64\"}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"hello\",\"session_id\":\"018b187a-7b35-444c-8d6f-92b90e8d7b64\",\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}'\n"
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake claude: %v", err)
	}

	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "")
	client := NewClientWithOptions("claude-sonnet-4-6", Options{
		Executable:           fakeClaude,
		WorkDir:              tmp,
		NoSessionPersistence: true,
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
		"--verbose",
		"--include-partial-messages",
		"--no-session-persistence",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("captured args missing %q: %s", want, joined)
		}
	}
}
