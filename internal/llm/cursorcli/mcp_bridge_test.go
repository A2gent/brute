package cursorcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestMCPBridgeInvocationRequiresSessionContext(t *testing.T) {
	called := false
	client := NewClientWithOptions("composer-2.5", Options{
		MCPBridge: func(ctx context.Context, sessionID string) (string, func(), error) {
			called = true
			return `{"mcpServers":{}}`, func() {}, nil
		},
	})

	inv := client.newMCPBridgeInvocation(context.Background())
	if called {
		t.Fatal("expected missing session id to skip the bridge hook")
	}
	if len(inv.args) != 0 {
		t.Fatalf("args = %v, want none", inv.args)
	}
}

func TestMCPBridgeInvocationWritesPluginAndRevokes(t *testing.T) {
	revoked := false
	client := NewClientWithOptions("composer-2.5", Options{
		MCPBridge: func(ctx context.Context, sessionID string) (string, func(), error) {
			if sessionID != "sess-1" {
				t.Fatalf("sessionID = %q", sessionID)
			}
			return `{
				"mcpServers": {
					"a2gent": {
						"type": "http",
						"url": "http://127.0.0.1:5445/mcp/sessions/sess-1",
						"headers": {"Authorization": "Bearer secret-token"}
					}
				}
			}`, func() { revoked = true }, nil
		},
	})

	ctx := context.WithValue(context.Background(), "session_id", "sess-1")
	inv := client.newMCPBridgeInvocation(ctx)
	t.Cleanup(inv.revoke)

	joined := strings.Join(inv.args, "\n")
	if !strings.Contains(joined, "--plugin-dir") || !strings.Contains(joined, "--approve-mcps") {
		t.Fatalf("args = %v, want --plugin-dir and --approve-mcps", inv.args)
	}
	pluginDir := ""
	for i, arg := range inv.args {
		if arg == "--plugin-dir" && i+1 < len(inv.args) {
			pluginDir = inv.args[i+1]
		}
	}
	if pluginDir == "" {
		t.Fatal("plugin dir missing from args")
	}

	manifestPath := filepath.Join(pluginDir, ".cursor-plugin", "plugin.json")
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("stat plugin manifest: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("plugin manifest mode = %o, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read plugin manifest: %v", err)
	}
	if strings.Contains(string(raw), "secret-token") == false || strings.Contains(string(raw), `"type"`) {
		t.Fatalf("manifest = %s, want bearer token and no type field", raw)
	}
	var manifest struct {
		Name       string `json:"name"`
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if manifest.Name != cursorMCPPluginName {
		t.Fatalf("plugin name = %q", manifest.Name)
	}
	server := manifest.MCPServers["a2gent"]
	if server.URL != "http://127.0.0.1:5445/mcp/sessions/sess-1" {
		t.Fatalf("url = %q", server.URL)
	}
	if server.Headers["Authorization"] != "Bearer secret-token" {
		t.Fatalf("authorization = %q", server.Headers["Authorization"])
	}

	args := client.buildArgs("composer-2.5", "hello", inv)
	if !strings.Contains(strings.Join(args, "\n"), "--approve-mcps") {
		t.Fatalf("buildArgs missing approve-mcps: %v", args)
	}

	inv.revoke()
	if !revoked {
		t.Fatal("expected token revoke")
	}
	if _, err := os.Stat(pluginDir); !os.IsNotExist(err) {
		t.Fatalf("plugin dir still present after revoke: %v", err)
	}
}

func TestMCPBridgeInvocationEmptyConfigDisables(t *testing.T) {
	client := NewClientWithOptions("composer-2.5", Options{
		MCPBridge: func(ctx context.Context, sessionID string) (string, func(), error) {
			return "", func() {}, nil
		},
	})
	ctx := context.WithValue(context.Background(), "session_id", "sess-1")
	inv := client.newMCPBridgeInvocation(ctx)
	if len(inv.args) != 0 {
		t.Fatalf("args = %v, want bridge disabled", inv.args)
	}
}

func TestMCPBridgePromptNoteOnlyWhenEnabled(t *testing.T) {
	request := &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}}
	plain := buildPrompt(request, false)
	if strings.Contains(plain, cursorMCPPluginID) {
		t.Fatalf("plain prompt should not mention the bridge: %s", plain)
	}
	bridged := buildPrompt(request, true)
	if !strings.Contains(bridged, cursorMCPPluginID) || !strings.Contains(bridged, "question") {
		t.Fatalf("bridged prompt missing MCP note: %s", bridged)
	}
}
