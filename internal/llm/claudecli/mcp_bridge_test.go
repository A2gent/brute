package claudecli

import (
	"context"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestMCPBridgeInvocationRequiresSessionContext(t *testing.T) {
	t.Parallel()

	hookCalled := false
	client := NewClientWithOptions("claude-sonnet-4-5", Options{
		MCPBridge: func(ctx context.Context, sessionID string) (string, func(), error) {
			hookCalled = true
			return `{"mcpServers":{}}`, func() {}, nil
		},
	})

	inv := client.newMCPBridgeInvocation(context.Background())
	if hookCalled {
		t.Fatal("hook must not be called without a session id in context")
	}
	if len(inv.args) != 0 || inv.allowedTools != "" {
		t.Fatalf("expected empty invocation, got %+v", inv)
	}
	inv.revoke() // must be a safe no-op
}

func TestMCPBridgeInvocationBuildsArgsAndRevoke(t *testing.T) {
	t.Parallel()

	revoked := false
	client := NewClientWithOptions("claude-sonnet-4-5", Options{
		MCPBridge: func(ctx context.Context, sessionID string) (string, func(), error) {
			if sessionID != "sess-1" {
				t.Fatalf("sessionID = %q, want sess-1", sessionID)
			}
			return `{"mcpServers":{"a2gent":{"type":"http"}}}`, func() { revoked = true }, nil
		},
	})

	ctx := context.WithValue(context.Background(), "session_id", "sess-1")
	inv := client.newMCPBridgeInvocation(ctx)
	joined := strings.Join(inv.args, " ")
	for _, want := range []string{"--mcp-config", "--strict-mcp-config", `"a2gent"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q missing %q", joined, want)
		}
	}
	if inv.allowedTools != "mcp__a2gent__.*" {
		t.Fatalf("allowedTools = %q", inv.allowedTools)
	}
	inv.revoke()
	if !revoked {
		t.Fatal("revoke callback was not invoked")
	}
}

func TestMCPBridgeInvocationEmptyConfigDisables(t *testing.T) {
	t.Parallel()

	client := NewClientWithOptions("claude-sonnet-4-5", Options{
		MCPBridge: func(ctx context.Context, sessionID string) (string, func(), error) {
			return "", nil, nil
		},
	})
	ctx := context.WithValue(context.Background(), "session_id", "sess-1")
	inv := client.newMCPBridgeInvocation(ctx)
	if len(inv.args) != 0 || inv.allowedTools != "" {
		t.Fatalf("expected empty invocation, got %+v", inv)
	}
}

func TestMCPBridgeArgsInStreamCommand(t *testing.T) {
	t.Parallel()

	client := NewClientWithOptions("claude-sonnet-4-5", Options{})
	bridge := mcpBridgeInvocation{
		args:         []string{"--mcp-config", `{"mcpServers":{"a2gent":{}}}`, "--strict-mcp-config"},
		allowedTools: "mcp__a2gent__.*",
		revoke:       func() {},
	}
	request := &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
		Tools:    []llm.ToolDefinition{{Name: "bash"}},
	}
	args := client.buildStreamArgs(request, "claude-sonnet-4-5", "prompt", bridge)
	joined := strings.Join(args, "\x00")

	if !strings.Contains(joined, "--mcp-config") || !strings.Contains(joined, "--strict-mcp-config") {
		t.Fatalf("bridge args missing: %v", args)
	}
	allowedIdx := -1
	for i, arg := range args {
		if arg == "--allowedTools" {
			allowedIdx = i
		}
	}
	if allowedIdx < 0 || allowedIdx+1 >= len(args) {
		t.Fatalf("--allowedTools missing: %v", args)
	}
	allowed := args[allowedIdx+1]
	if !strings.Contains(allowed, "Bash") || !strings.Contains(allowed, "mcp__a2gent__.*") {
		t.Fatalf("allowedTools = %q, want Bash + mcp wildcard", allowed)
	}
}

func TestMCPBridgeAllowedToolsWithoutNativeTools(t *testing.T) {
	t.Parallel()

	client := NewClientWithOptions("claude-sonnet-4-5", Options{})
	bridge := mcpBridgeInvocation{
		args:         []string{"--mcp-config", `{}`},
		allowedTools: "mcp__a2gent__.*",
		revoke:       func() {},
	}
	request := &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	}
	args := client.buildArgs(request, "claude-sonnet-4-5", "prompt", bridge)
	joined := strings.Join(args, ",")
	if !strings.Contains(joined, "mcp__a2gent__.*") {
		t.Fatalf("mcp wildcard missing from args: %v", args)
	}
}
