package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/A2gent/brute/internal/tools"
)

// Opt-in: verifies the real Claude HTTP MCP handshake and spends subscription quota.
func TestLiveHostClaudeMCP(t *testing.T) {
	if os.Getenv("AAGENT_CLAUDE_MCP_LIVE") != "1" {
		t.Skip("set AAGENT_CLAUDE_MCP_LIVE=1 to exercise real Claude MCP calls")
	}
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://127.0.0.1:1/v1")
	t.Setenv("AAGENT_CLAUDE_AGENT_SDK_SIDECAR_PATH", "")
	defer claudecli.ClosePersistentProcesses()
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistent_%t", persistent), func(t *testing.T) {
			t.Setenv("AAGENT_CLAUDE_CLI_PERSISTENT", strconv.FormatBool(persistent))
			s := newMCPBridgeTestServer(t)
			s.config.HostClaudeCLI = true
			s.config.WorkDir = t.TempDir()
			sess, err := s.sessionManager.Create("build")
			if err != nil {
				t.Fatal(err)
			}
			called := make(chan string, 4)
			sentinel := "MCP_SESSION_OK_" + sess.ID
			s.toolManager.Register(&bridgeDelegationProbe{name: "bridge_mcp_probe", execute: func(ctx context.Context, _ json.RawMessage) (*tools.Result, error) {
				id, _ := ctx.Value("session_id").(string)
				called <- id
				return &tools.Result{Success: true, Output: sentinel}, nil
			}})
			endpoint := httptest.NewServer(s.router)
			defer endpoint.Close()
			_, port, _ := net.SplitHostPort(endpoint.Listener.Addr().String())
			s.port, _ = strconv.Atoi(port)
			client, err := s.createBaseLLMClientForSession(config.ProviderAnthropic, "sonnet", sess)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := client.(*claudecli.Client); !ok {
				t.Fatalf("expected child-local Claude, got %T", client)
			}
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), "session_id", sess.ID), 90*time.Second)
			defer cancel()
			resp, err := client.Chat(ctx, &llm.ChatRequest{
				SessionID: sess.ID,
				Messages:  []llm.Message{{Role: "user", Content: "Call mcp__a2gent__bridge_mcp_probe exactly once with {} and reply with its exact returned text. Do not use any other tools."}},
				Tools:     []llm.ToolDefinition{{Name: "bridge_mcp_probe", Description: "Return the session test marker", InputSchema: map[string]interface{}{"type": "object"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case id := <-called:
				if id != sess.ID {
					t.Fatalf("MCP tool ran in session %q, want %q", id, sess.ID)
				}
			default:
				t.Fatalf("Claude did not call its child MCP tool; response=%s", resp.Content)
			}
			if !strings.Contains(resp.Content, sentinel) {
				t.Fatalf("tool result not returned: %s", resp.Content)
			}
			t.Logf("Claude MCP tool completed in its owning child session (persistent=%t)", persistent)
		})
	}
}
