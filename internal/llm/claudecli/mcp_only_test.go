package claudecli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/approval"
	"github.com/A2gent/brute/internal/llm"
)

func TestMCPOnlyOptionsAndFlags(t *testing.T) {
	t.Setenv("AAGENT_CLAUDE_CLI_PERSISTENT", "1")
	t.Setenv(envSidecarPath, "/should/not/run")
	c := NewClientWithOptions("sonnet", Options{MCPOnly: true, Persistent: true, SidecarPath: "/sidecar", Broker: new(approval.Broker)})
	req := &llm.ChatRequest{SessionID: "session", ProviderSessionCursor: BindProviderSessionCursor("", "resume-id"), Tools: []llm.ToolDefinition{{Name: "bash"}, {Name: "read_file"}}}
	if !c.options.NoSessionPersistence || c.options.Persistent || c.options.SidecarPath != "" || c.options.Broker != nil || SidecarEnabled(c.options) || c.persistentEnabled(req) {
		t.Fatalf("unsafe options: %+v", c.options)
	}
	if c.providerSessionCursor("cursor") != "" {
		t.Error("session cursor leaked")
	}
	for _, bridge := range []mcpBridgeInvocation{{}, {args: []string{"--mcp-config", "{}", "--strict-mcp-config"}, allowedTools: "mcp__a2gent__*"}} {
		for _, args := range [][]string{c.buildArgs(req, "sonnet", "prompt", bridge), c.buildStreamArgs(req, "sonnet", "prompt", bridge)} {
			joined := strings.Join(args, "\x00")
			if !strings.Contains(joined, "--tools\x00\x00") || !strings.Contains(joined, "--strict-mcp-config") || !strings.Contains(joined, "--no-session-persistence") || strings.Contains(joined, "--resume") {
				t.Fatalf("unsafe flags: %q", args)
			}
			for i, arg := range args {
				if arg == "--allowedTools" && args[i+1] != "mcp__a2gent__*" {
					t.Fatalf("native tool allowed: %v", args)
				}
			}
			if len(bridge.args) > 0 && !strings.Contains(joined, "--allowedTools\x00mcp__a2gent__*") {
				t.Fatal("bridge tools missing")
			}
		}
	}
}

func TestMCPOnlyFailsClosed(t *testing.T) {
	hookErr := errors.New("bridge unavailable")
	for _, mode := range []string{"missing-hook", "missing-session", "empty-config", "hook-error"} {
		for _, stream := range []bool{false, true} {
			t.Run(mode+map[bool]string{true: "/stream", false: "/chat"}[stream], func(t *testing.T) {
				revoked := 0
				opts := Options{MCPOnly: true, Executable: "/must-not-spawn"}
				if mode != "missing-hook" {
					opts.MCPBridge = func(context.Context, string) (string, func(), error) {
						revoke := func() { revoked++ }
						if mode == "hook-error" {
							return "", revoke, hookErr
						}
						return "", revoke, nil
					}
				}
				ctx := context.Background()
				if mode != "missing-session" {
					ctx = context.WithValue(ctx, "session_id", "session")
				}
				c := NewClientWithOptions("sonnet", opts)
				var err error
				if stream {
					_, err = c.ChatStream(ctx, &llm.ChatRequest{}, func(llm.StreamEvent) error { return nil })
				} else {
					_, err = c.Chat(ctx, &llm.ChatRequest{})
				}
				if err == nil || !strings.Contains(err.Error(), "MCP-only") {
					t.Fatalf("did not fail closed: %v", err)
				}
				if mode == "hook-error" && !errors.Is(err, hookErr) {
					t.Error("lost hook error")
				}
				want := 0
				if mode == "empty-config" || mode == "hook-error" {
					want = 1
				}
				if revoked != want {
					t.Errorf("revoked=%d, want %d", revoked, want)
				}
			})
		}
	}
}
