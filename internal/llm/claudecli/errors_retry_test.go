package claudecli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestClaudeCLIErrorAfterProcessStartIsUnsafeForRetry(t *testing.T) {
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "")

	tmp := t.TempDir()
	fakeClaude := filepath.Join(tmp, "claude")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"error\",\"is_error\":true,\"error\":\"tool execution may have started\"}'\n" +
		"exit 1\n"
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake claude: %v", err)
	}

	client := NewClientWithOptions("claude-sonnet-4-6", Options{
		Executable: fakeClaude,
		WorkDir:    tmp,
	})
	_, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error from failing Claude CLI process")
	}
	if !llm.IsUnsafeForRetry(err) {
		t.Fatalf("post-start Claude CLI error should be unsafe for retry, got: %v", err)
	}
}

func TestClaudeCLIPreStartFailureRemainsRetrySafe(t *testing.T) {
	client := NewClientWithOptions("claude-sonnet-4-6", Options{
		Executable: "/definitely/missing/claude",
		WorkDir:    t.TempDir(),
	})
	_, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected pre-start error")
	}
	if llm.IsUnsafeForRetry(err) {
		t.Fatalf("pre-start failure should not be unsafe for retry, got: %v", err)
	}
}
