package antigravitycli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestBuildArgsUsesHeadlessStreamJSON(t *testing.T) {
	t.Setenv("AAGENT_ANTIGRAVITY_CLI_SKIP_PERMISSIONS", "")
	client := NewClientWithOptions("gemini-3.8-flash-low", Options{WorkDir: t.TempDir()})
	got := strings.Join(client.buildArgs("gemini-3.8-flash-low", "hi"), " ")
	want := "--print hi --output-format stream-json --model gemini-3.8-flash-low --dangerously-skip-permissions"
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}

	t.Setenv("AAGENT_ANTIGRAVITY_CLI_SKIP_PERMISSIONS", "false")
	client = NewClientWithOptions("", Options{WorkDir: t.TempDir()})
	if strings.Contains(strings.Join(client.buildArgs(client.model, "hi"), " "), "--dangerously-skip-permissions") {
		t.Fatal("skip-permissions must be disabled by env")
	}
}

func TestNormalizeModelMapsGeminiAPIIDs(t *testing.T) {
	cases := map[string]string{
		"":                       DefaultModel,
		"gemini-4-hydrogen":      "gemini-4-hydrogen-medium",
		"gemini-4-neon-high":     "gemini-4-neon-high",
		"google/gemini-4-neon":   "gemini-4-neon-medium",
		"claude-sonnet-5-5-high": "claude-sonnet-5-5-high",
	}
	for in, want := range cases {
		if got := NormalizeModel(in); got != want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseModelList(t *testing.T) {
	got := parseModelList("Fetching available models...\ngemini-4-neon-high\tGemini 4 Neon (High)\ngemini-3.8-flash-low\tGemini 3.8 Flash (Low)\n")
	if strings.Join(got, ",") != "gemini-4-neon-high,gemini-3.8-flash-low" {
		t.Fatalf("unexpected models: %v", got)
	}
}

const fakeStream = `{"event":"init","conversation_id":"c1","init":{"model":"gemini-3.8-flash-low","cwd":"/tmp"}}
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"ls"}}}}
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"ls"},"output":"a.txt"}}}
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"ACTIVE","step_type":"agent_response","text_delta":"PO"}}
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"DONE","step_type":"agent_response","text_delta":"NG"}}
{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"PONG\n","usage":{"input_tokens":10,"output_tokens":2,"thinking_tokens":1,"cache_read_tokens":5}}}`

func writeFakeAgy(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agy")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AAGENT_ANTIGRAVITY_CLI_PATH", path)
}

func TestChatStreamParsesFakeAgyOutput(t *testing.T) {
	streamFile := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(streamFile, []byte(fakeStream+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	writeFakeAgy(t, `printf '%s\n' "$@" > `+argsFile+`
cat `+streamFile+"\n")

	client := NewClientWithOptions("gemini-4-hydrogen", Options{WorkDir: t.TempDir()})
	var events []llm.StreamEvent
	resp, err := client.ChatStream(context.Background(), &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "ping"}}}, func(e llm.StreamEvent) error {
		events = append(events, e)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.Content != "PONG" || resp.ResponseID != "c1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Usage.InputTokens != 15 || resp.Usage.CachedInputTokens != 5 || resp.Usage.OutputTokens != 2 || resp.Usage.ReasoningTokens != 1 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, string(e.Type))
	}
	if got := strings.Join(kinds, ","); got != "tool_started,tool_output,tool_completed,content_delta,content_delta,usage" {
		t.Fatalf("unexpected events: %s", got)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--model\ngemini-4-hydrogen-medium\n") {
		t.Fatalf("model not normalized in args:\n%s", args)
	}
}

func TestChatReportsAuthError(t *testing.T) {
	writeFakeAgy(t, `echo 'Error: authentication timed out.'
echo '{"event":"result","result":{"status":"ERROR","error":"authentication failed or timed out"}}'
exit 1
`)
	client := NewClientWithOptions("", Options{WorkDir: t.TempDir()})
	_, err := client.Chat(context.Background(), &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") || !strings.Contains(err.Error(), "Google Sign-In") {
		t.Fatalf("expected auth error with hint, got %v", err)
	}
}

func TestChatRejectsImageAttachments(t *testing.T) {
	writeFakeAgy(t, "echo should-not-run; exit 1\n")
	client := NewClientWithOptions("", Options{WorkDir: t.TempDir()})
	_, err := client.Chat(context.Background(), &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "what is this", Images: []llm.Image{{MediaType: "image/png"}}}}})
	if !errors.Is(err, ErrImagesUnsupported) {
		t.Fatalf("expected ErrImagesUnsupported, got %v", err)
	}
}
