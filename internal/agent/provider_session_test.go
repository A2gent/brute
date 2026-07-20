package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestUseProviderSessionPersistsCursorAcrossTwoTurns(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	mockLLM := &MockLLM{
		Responses: []*llm.ChatResponse{
			{Content: "hi", ProviderSessionCursor: "cursor-turn-1"},
			{Content: "follow up answer", ProviderSessionCursor: "cursor-turn-2"},
		},
	}
	ag := New(Config{
		MaxSteps:           10,
		UseProviderSession: true,
	}, mockLLM, tools.NewManager(t.TempDir()), sm)

	sess.AddUserMessage("hello")
	if _, _, err := ag.RunWithEvents(context.Background(), sess, "hello", nil); err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}

	if got, _ := sess.Metadata[metadataProviderSessionCursor].(string); got != "cursor-turn-1" {
		t.Fatalf("session metadata provider cursor = %q, want cursor-turn-1", got)
	}
	lastAssistant := lastAssistantMessage(sess)
	if lastAssistant == nil {
		t.Fatal("expected assistant message after turn 1")
	}
	if got, _ := lastAssistant.Metadata[messageMetadataProviderSessionCursor].(string); got != "cursor-turn-1" {
		t.Fatalf("assistant metadata provider cursor = %q, want cursor-turn-1", got)
	}

	reloaded, err := sm.Get(sess.ID)
	if err != nil {
		t.Fatalf("failed to reload session: %v", err)
	}
	if got, _ := reloaded.Metadata[metadataProviderSessionCursor].(string); got != "cursor-turn-1" {
		t.Fatalf("persisted session metadata provider cursor = %q, want cursor-turn-1", got)
	}

	reloaded.AddUserMessage("follow up")
	if _, _, err := ag.RunWithEvents(context.Background(), reloaded, "follow up", nil); err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}

	if len(mockLLM.CapturedRequests) < 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(mockLLM.CapturedRequests))
	}
	secondReq := mockLLM.CapturedRequests[1]
	if secondReq.ProviderSessionCursor != "cursor-turn-1" {
		t.Fatalf("second request ProviderSessionCursor = %q, want cursor-turn-1", secondReq.ProviderSessionCursor)
	}
	if secondReq.PreviousResponseID != "" {
		t.Fatalf("second request PreviousResponseID = %q, want empty", secondReq.PreviousResponseID)
	}
	if len(secondReq.Messages) != 1 || secondReq.Messages[0].Content != "follow up" {
		t.Fatalf("second request messages = %+v, want only latest user message", secondReq.Messages)
	}
	if got, _ := reloaded.Metadata[metadataProviderSessionCursor].(string); got != "cursor-turn-2" {
		t.Fatalf("updated session metadata provider cursor = %q, want cursor-turn-2", got)
	}
}

func TestClaudeProviderSessionResumesAcrossTwoTurnsWithIdentity(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	callFile := filepath.Join(tmp, "calls.txt")
	fakeClaude := writeAgentFakeClaude(t, tmp, argsFile, callFile)
	identity := "claude:test-instance"
	client := claudecli.NewClientWithOptions("claude-sonnet-4-6", claudecli.Options{
		Executable: fakeClaude,
		WorkDir:    tmp,
		Identity:   identity,
	})
	ag := New(Config{
		MaxSteps:                10,
		UseProviderSession:      true,
		ProviderSessionIdentity: identity,
	}, client, tools.NewManager(t.TempDir()), sm)

	sess.AddUserMessage("first prompt")
	if _, _, err := ag.RunWithEvents(context.Background(), sess, "first prompt", nil); err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}
	if got, _ := sess.Metadata[metadataProviderSessionCursor].(string); got != "claude-session-1" {
		t.Fatalf("persisted provider cursor = %q, want raw cursor", got)
	}

	reloaded, err := sm.Get(sess.ID)
	if err != nil {
		t.Fatalf("failed to reload session: %v", err)
	}
	reloaded.AddUserMessage("second prompt")
	if _, _, err := ag.RunWithEvents(context.Background(), reloaded, "second prompt", nil); err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}

	calls := readAgentFakeClaudeCalls(t, argsFile)
	if len(calls) != 2 {
		t.Fatalf("fake Claude calls = %d, want 2: %#v", len(calls), calls)
	}
	assertClaudeArgsContainPair(t, calls[1], "--resume", "claude-session-1")
	if prompt := claudePromptArg(t, calls[1]); prompt != "second prompt" {
		t.Fatalf("turn 2 prompt = %q, want delta prompt only", prompt)
	}
	if strings.Contains(strings.Join(calls[1], "\n"), "first prompt") {
		t.Fatalf("turn 2 args unexpectedly contain prior transcript: %#v", calls[1])
	}
}

func TestClaudeProviderSessionCompactionStartsFreshSessionWithSummary(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	callFile := filepath.Join(tmp, "calls.txt")
	fakeClaude := writeAgentFakeClaude(t, tmp, argsFile, callFile)
	identity := "claude:test-instance"
	client := claudecli.NewClientWithOptions("claude-sonnet-4-6", claudecli.Options{
		Executable: fakeClaude,
		WorkDir:    tmp,
		Identity:   identity,
	})
	ag := New(Config{
		MaxSteps:                 10,
		ContextWindow:            100,
		CompactionTriggerPercent: 50,
		UseProviderSession:       true,
		ProviderSessionIdentity:  identity,
	}, client, tools.NewManager(t.TempDir()), sm)

	sess.AddUserMessage("first prompt")
	if _, _, err := ag.RunWithEvents(context.Background(), sess, "first prompt", nil); err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}
	metadataSetFloat(sess, metadataCurrentContextTokens, 60)
	sess.AddUserMessage("continue after compaction")
	if _, _, err := ag.RunWithEvents(context.Background(), sess, "continue after compaction", nil); err != nil {
		t.Fatalf("compacted turn failed: %v", err)
	}

	calls := readAgentFakeClaudeCalls(t, argsFile)
	if len(calls) != 3 {
		t.Fatalf("fake Claude calls = %d, want initial, compaction, continuation: %#v", len(calls), calls)
	}
	if joined := strings.Join(calls[2], "\n"); strings.Contains(joined, "--resume") {
		t.Fatalf("post-compaction request must start a fresh Claude session, got args: %#v", calls[2])
	}
	prompt := claudePromptArg(t, calls[2])
	if !strings.Contains(prompt, "compacted summary") || !strings.Contains(prompt, "continue after compaction") {
		t.Fatalf("post-compaction prompt must contain summary and pending user message, got %q", prompt)
	}
	if strings.Contains(prompt, "first prompt") {
		t.Fatalf("post-compaction prompt unexpectedly duplicates summarized transcript: %q", prompt)
	}
	if got, _ := sess.Metadata[metadataProviderSessionCursor].(string); got != "claude-session-3" {
		t.Fatalf("post-compaction provider cursor = %q, want new raw cursor", got)
	}
	if got, _ := sess.Metadata[metadataProviderSessionIdentity].(string); got != identity {
		t.Fatalf("post-compaction provider identity = %q, want %q", got, identity)
	}
}

func writeAgentFakeClaude(t *testing.T, tmp, argsFile, callFile string) string {
	t.Helper()
	fakeClaude := filepath.Join(tmp, "claude")
	script := `#!/bin/sh
call=1
if [ -f "$CALL_FILE" ]; then call=$(($(cat "$CALL_FILE") + 1)); fi
printf '%s' "$call" > "$CALL_FILE"
printf '%s\0' "$@" >> "$ARGS_FILE"
printf '\036' >> "$ARGS_FILE"
if [ "$call" -eq 2 ]; then result="compacted summary"; else result="answer $call"; fi
printf '{"type":"result","subtype":"success","result":"%s","session_id":"claude-session-%s","usage":{"input_tokens":1,"output_tokens":1}}\n' "$result" "$call"
`
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake Claude: %v", err)
	}
	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("CALL_FILE", callFile)
	return fakeClaude
}

func readAgentFakeClaudeCalls(t *testing.T, argsFile string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("failed to read fake Claude args: %v", err)
	}
	frames := strings.Split(strings.TrimSuffix(string(raw), "\x1e"), "\x1e")
	calls := make([][]string, 0, len(frames))
	for _, frame := range frames {
		if frame == "" {
			continue
		}
		fields := strings.Split(strings.TrimSuffix(frame, "\x00"), "\x00")
		calls = append(calls, fields)
	}
	return calls
}

func assertClaudeArgsContainPair(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return
		}
	}
	t.Fatalf("Claude args missing %s %s: %#v", flag, value, args)
}

func claudePromptArg(t *testing.T, args []string) string {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-p" {
			return args[i+1]
		}
	}
	t.Fatal(fmt.Sprintf("Claude args missing -p prompt: %#v", args))
	return ""
}

func lastAssistantMessage(sess *session.Session) *session.Message {
	if sess == nil {
		return nil
	}
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role == "assistant" {
			return &sess.Messages[i]
		}
	}
	return nil
}
