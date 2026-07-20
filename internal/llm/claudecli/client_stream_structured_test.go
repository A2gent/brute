package claudecli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

type runtimeContractEvent struct {
	Type string

	ReasoningDelta string
	ContentDelta   string

	ToolCallID   string
	ToolCallName string
	ToolIndex    int
	ToolInput    string

	ToolOutputContent string
	ToolOutputIsError bool

	TotalCostUSD  float64
	DurationMS    int64
	DurationAPIMS int64
	NumTurns      int

	WarningStatus string
	WarningText   string
}

func TestClientChatStreamStructuredRuntimeEventsFromFixture(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeClaude := writeFakeClaudeStreamFromFixture(t, tmp, argsFile, "testdata/stream_structured_success.ndjson")

	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "")
	client := NewClientWithOptions("claude-sonnet-4-6", Options{
		Executable:           fakeClaude,
		WorkDir:              tmp,
		NoSessionPersistence: true,
	})

	var events []llm.StreamEvent
	resp, err := client.ChatStream(t.Context(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "read foo.go"}},
	}, func(event llm.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}

	want := []runtimeContractEvent{
		{Type: "runtime_warning", WarningStatus: "rate_limited", WarningText: "Retrying after rate limit"},
		{Type: "reasoning_delta", ReasoningDelta: "Checking"},
		{Type: "reasoning_delta", ReasoningDelta: " the file."},
		{Type: "content_delta", ContentDelta: "I'll read it."},
		{Type: "tool_started", ToolCallID: "toolu_read_01", ToolCallName: "Read", ToolIndex: 2},
		{Type: "tool_updated", ToolCallID: "toolu_read_01", ToolInput: `{"file_path":`},
		{Type: "tool_updated", ToolCallID: "toolu_read_01", ToolInput: `{"file_path":"foo.go"}`},
		{Type: "tool_completed", ToolCallID: "toolu_read_01", ToolCallName: "Read", ToolInput: `{"file_path":"foo.go"}`},
		{
			Type:              "tool_output",
			ToolCallID:        "toolu_read_01",
			ToolCallName:      "Read",
			ToolOutputContent: "package main\n",
			ToolOutputIsError: false,
		},
		{Type: "runtime_warning", WarningStatus: "compact_boundary"},
		{Type: "cost", TotalCostUSD: 0.0123, DurationMS: 4500, DurationAPIMS: 4100, NumTurns: 1},
		{Type: "usage"},
	}

	if err := assertRuntimeContract(events, want); err != nil {
		t.Fatalf("runtime contract mismatch: %v\ngot events: %s", err, formatStreamEvents(events))
	}

	if resp.Content != "Read foo.go successfully." {
		t.Fatalf("content = %q, want %q", resp.Content, "Read foo.go successfully.")
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("ToolCalls = %#v, want empty because Claude native tool was already executed", resp.ToolCalls)
	}
	if resp.ProviderSessionCursor != "" {
		t.Fatalf("ProviderSessionCursor = %q, want empty with NoSessionPersistence", resp.ProviderSessionCursor)
	}
	if resp.Usage.InputTokens != 1200 || resp.Usage.OutputTokens != 85 {
		t.Fatalf("usage = %+v, want input=1200 output=85", resp.Usage)
	}
}

func TestClientChatStreamMalformedNDJSONIsUnsafeForRetry(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeClaude := writeFakeClaudeStreamLines(t, tmp, argsFile,
		`not valid ndjson`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"partial"}}}`,
	)

	t.Setenv("ARGS_FILE", argsFile)
	client := NewClientWithOptions("claude-sonnet-4-6", Options{
		Executable:           fakeClaude,
		WorkDir:              tmp,
		NoSessionPersistence: true,
	})

	_, err := client.ChatStream(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
	}, nil)
	if err == nil {
		t.Fatal("expected malformed NDJSON to fail")
	}
	if !strings.Contains(err.Error(), "failed to parse Claude CLI stream line") {
		t.Fatalf("error = %q, want parse failure", err.Error())
	}
	if !llm.IsUnsafeForRetry(err) {
		t.Fatalf("post-start parse error should be unsafe for retry, got: %v", err)
	}
}

func TestClientChatStreamCancellation(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	fakeClaude := writeFakeClaudeSlowStreamLines(t, tmp, argsFile,
		`{"type":"stream_event","session_id":"018b187a-7b35-444c-8d6f-92b90e8d7b64","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}}`,
		`{"type":"result","subtype":"success","result":"done","session_id":"018b187a-7b35-444c-8d6f-92b90e8d7b64","usage":{"input_tokens":1,"output_tokens":1}}`,
	)

	t.Setenv("ARGS_FILE", argsFile)
	client := NewClientWithOptions("claude-sonnet-4-6", Options{
		Executable:           fakeClaude,
		WorkDir:              tmp,
		NoSessionPersistence: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := client.ChatStream(ctx, &llm.ChatRequest{
			Messages: []llm.Message{{Role: "user", Content: "hello"}},
		}, func(event llm.StreamEvent) error {
			if event.Type == llm.StreamEventContentDelta {
				cancel()
			}
			return nil
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cancelled ChatStream")
	}
}

func writeFakeClaudeStreamFromFixture(t *testing.T, tmp, argsFile, fixtureRelPath string) string {
	t.Helper()
	raw := readStreamFixture(t, fixtureRelPath)
	return writeFakeClaudeStreamScript(t, tmp, argsFile, string(raw))
}

func writeFakeClaudeSlowStreamLines(t *testing.T, tmp, argsFile string, lines ...string) string {
	t.Helper()
	fixtureFile := filepath.Join(tmp, "stream.ndjson")
	if err := os.WriteFile(fixtureFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write stream fixture: %v", err)
	}
	script := "#!/bin/sh\n" +
		": > \"$ARGS_FILE\"\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"$ARGS_FILE\"; done\n" +
		"while IFS= read -r line; do\n" +
		"  printf '%s\\n' \"$line\"\n" +
		"  sleep 2\n" +
		"done < \"$FIXTURE_FILE\"\n"
	return installFakeClaudeScript(t, tmp, script, fixtureFile)
}

func writeFakeClaudeStreamLines(t *testing.T, tmp, argsFile string, lines ...string) string {
	t.Helper()
	return writeFakeClaudeStreamScript(t, tmp, argsFile, strings.Join(lines, "\n")+"\n")
}

func writeFakeClaudeStreamScript(t *testing.T, tmp, argsFile, stdout string) string {
	t.Helper()
	fixtureFile := filepath.Join(tmp, "stream.ndjson")
	if err := os.WriteFile(fixtureFile, []byte(stdout), 0o644); err != nil {
		t.Fatalf("write stream fixture: %v", err)
	}
	script := "#!/bin/sh\n" +
		": > \"$ARGS_FILE\"\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"$ARGS_FILE\"; done\n" +
		"cat \"$FIXTURE_FILE\"\n"
	return installFakeClaudeScript(t, tmp, script, fixtureFile)
}

func installFakeClaudeScript(t *testing.T, tmp, script, fixtureFile string) string {
	t.Helper()
	fakeClaude := filepath.Join(tmp, "claude")
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("FIXTURE_FILE", fixtureFile)
	return fakeClaude
}

func readStreamFixture(t *testing.T, fixtureRelPath string) []byte {
	t.Helper()
	raw, err := os.ReadFile(fixtureRelPath)
	if err != nil {
		t.Fatalf("read fixture %q: %v", fixtureRelPath, err)
	}
	return raw
}

func assertRuntimeContract(got []llm.StreamEvent, want []runtimeContractEvent) error {
	filtered := make([]llm.StreamEvent, 0, len(got))
	for _, ev := range got {
		if ev.Type == llm.StreamEventProviderTrace {
			continue
		}
		filtered = append(filtered, ev)
	}

	if len(filtered) < len(want) {
		return fmt.Errorf("got %d runtime events, want at least %d", len(filtered), len(want))
	}

	wi := 0
	for _, ev := range filtered {
		if wi >= len(want) {
			break
		}
		if err := matchRuntimeContractEvent(ev, want[wi]); err != nil {
			continue
		}
		wi++
	}
	if wi < len(want) {
		return fmt.Errorf("missing contract events after index %d (want %d total); next expected %+v", wi, len(want), want[wi])
	}
	return nil
}

func matchRuntimeContractEvent(got llm.StreamEvent, want runtimeContractEvent) error {
	gotType := string(got.Type)
	if gotType != want.Type {
		return fmt.Errorf("type %q != %q", gotType, want.Type)
	}

	switch want.Type {
	case "reasoning_delta":
		if got.ReasoningDelta != want.ReasoningDelta {
			return fmt.Errorf("reasoning_delta %q != %q", got.ReasoningDelta, want.ReasoningDelta)
		}
	case "content_delta":
		if got.ContentDelta != want.ContentDelta {
			return fmt.Errorf("content_delta %q != %q", got.ContentDelta, want.ContentDelta)
		}
	case "tool_started":
		if got.ToolCallID != want.ToolCallID || got.ToolCallName != want.ToolCallName || got.ToolCallIndex != want.ToolIndex {
			return fmt.Errorf("tool_started = id:%q name:%q index:%d, want id:%q name:%q index:%d",
				got.ToolCallID, got.ToolCallName, got.ToolCallIndex, want.ToolCallID, want.ToolCallName, want.ToolIndex)
		}
	case "tool_updated", "tool_completed":
		if got.ToolCallID != want.ToolCallID {
			return fmt.Errorf("%s tool id %q != %q", want.Type, got.ToolCallID, want.ToolCallID)
		}
		if want.Type == "tool_completed" && got.ToolCallName != want.ToolCallName {
			return fmt.Errorf("tool_completed name %q != %q", got.ToolCallName, want.ToolCallName)
		}
		if got.ToolInputDelta != want.ToolInput {
			return fmt.Errorf("%s input %q != %q", want.Type, got.ToolInputDelta, want.ToolInput)
		}
	case "tool_output":
		if got.ToolCallID != want.ToolCallID || got.ToolCallName != want.ToolCallName {
			return fmt.Errorf("tool_output identity mismatch: got id:%q name:%q", got.ToolCallID, got.ToolCallName)
		}
		if got.ToolOutput != want.ToolOutputContent || got.ToolIsError != want.ToolOutputIsError {
			return fmt.Errorf("tool_output content/error = %q/%v, want %q/%v", got.ToolOutput, got.ToolIsError, want.ToolOutputContent, want.ToolOutputIsError)
		}
	case "cost":
		if got.TotalCostUSD != want.TotalCostUSD || got.DurationMS != want.DurationMS ||
			got.DurationAPIMS != want.DurationAPIMS || got.NumTurns != want.NumTurns {
			return fmt.Errorf("cost = total:%v duration:%d api:%d turns:%d, want total=%v duration=%d api=%d turns=%d",
				got.TotalCostUSD, got.DurationMS, got.DurationAPIMS, got.NumTurns,
				want.TotalCostUSD, want.DurationMS, want.DurationAPIMS, want.NumTurns)
		}
	case "runtime_warning":
		if got.RuntimeStatus != want.WarningStatus || (want.WarningText != "" && got.RuntimeWarning != want.WarningText) {
			return fmt.Errorf("runtime_warning = %q/%q, want %q/%q", got.RuntimeStatus, got.RuntimeWarning, want.WarningStatus, want.WarningText)
		}
	case "usage":
	default:
		return fmt.Errorf("unknown contract type %q", want.Type)
	}
	return nil
}

func formatStreamEvents(events []llm.StreamEvent) string {
	var b strings.Builder
	for i, ev := range events {
		fmt.Fprintf(&b, "%d:%s", i, ev.Type)
		if ev.ContentDelta != "" {
			fmt.Fprintf(&b, "(delta=%q)", ev.ContentDelta)
		}
		if ev.ReasoningDelta != "" {
			fmt.Fprintf(&b, "(reasoning=%q)", ev.ReasoningDelta)
		}
		if ev.ToolCallID != "" {
			fmt.Fprintf(&b, "(tool=%s/%s)", ev.ToolCallName, ev.ToolCallID)
		}
		b.WriteString("; ")
	}
	return b.String()
}
