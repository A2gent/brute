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

func TestMCPOnlyInvocationCreatesIsolatedCursorWorkspace(t *testing.T) {
	workDir := t.TempDir()
	client := NewClientWithOptions("composer-2.5", Options{WorkDir: t.TempDir()})
	isolated := client.forInvocation(WithMCPOnly(context.Background(), workDir))
	if !isolated.options.MCPOnly || isolated.options.WorkDir != workDir || isolated.options.Sandbox != "enabled" || isolated.options.Force || !isolated.options.Trust {
		t.Fatalf("isolated options = %+v", isolated.options)
	}
	if err := isolated.prepareMCPOnlyWorkspace(); err != nil {
		t.Fatal(err)
	}

	cliPath := filepath.Join(workDir, ".cursor", "cli.json")
	cliInfo, err := os.Stat(cliPath)
	if err != nil {
		t.Fatal(err)
	}
	if cliInfo.Mode().Perm() != 0o600 {
		t.Fatalf("cli.json mode = %o, want 0600", cliInfo.Mode().Perm())
	}
	var cliConfig cursorCLIConfig
	data, err := os.ReadFile(cliPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cliConfig); err != nil {
		t.Fatal(err)
	}
	if cliConfig.Version != 1 || len(cliConfig.Permissions.Allow) != 1 || cliConfig.Permissions.Allow[0] != "Mcp(plugin-a2gent-bridge-a2gent:*)" {
		t.Fatalf("unexpected project CLI permissions: %+v", cliConfig)
	}
	for _, denied := range []string{"Shell(*)", "Read(/**)", "Write(/**)", "WebFetch(*)"} {
		if !containsString(cliConfig.Permissions.Deny, denied) {
			t.Errorf("missing deny permission %q in %v", denied, cliConfig.Permissions.Deny)
		}
	}

	var sandbox cursorProjectSandboxConfig
	sandboxData, err := os.ReadFile(filepath.Join(workDir, ".cursor", "sandbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(sandboxData, &sandbox); err != nil {
		t.Fatal(err)
	}
	if sandbox.Type != "workspace_readwrite" || sandbox.ReadBoundary != "workspace" || !sandbox.DisableTmpWrite {
		t.Fatalf("unexpected sandbox config: %+v", sandbox)
	}

	t.Setenv("CURSOR_CONFIG_DIR", "/parent/cursor/config")
	configDir := filepath.Join(workDir, ".cursor-cli-config")
	if got := environmentValue(isolated.commandEnv(), "CURSOR_CONFIG_DIR"); got != configDir {
		t.Fatalf("CURSOR_CONFIG_DIR = %q, want isolated directory %q", got, configDir)
	}
	var globalConfig cursorCLIConfig
	globalData, err := os.ReadFile(filepath.Join(configDir, "cli-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(globalData, &globalConfig); err != nil {
		t.Fatal(err)
	}
	if globalConfig.ApprovalMode != "allowlist" || globalConfig.Sandbox == nil || globalConfig.Sandbox.Mode != "enabled" || globalConfig.Sandbox.ReadBoundary != "workspace" {
		t.Fatalf("unexpected isolated global CLI config: %+v", globalConfig)
	}

	prompt := buildPrompt(&llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "inspect"}}}, true, true)
	if !strings.Contains(prompt, "Do not use Cursor's native file, shell, or web tools") || !strings.Contains(prompt, cursorMCPPluginID) {
		t.Fatalf("isolated prompt missing tool confinement instructions: %s", prompt)
	}
	if strings.Contains(prompt, "Use Cursor's native tools directly") {
		t.Fatalf("isolated prompt still encourages native tools: %s", prompt)
	}
}

func TestMCPOnlyBridgeDoesNotApproveOtherServers(t *testing.T) {
	client := NewClientWithOptions("composer-2.5", Options{
		MCPOnly: true,
		MCPBridge: func(context.Context, string) (string, func(), error) {
			return `{"mcpServers":{"a2gent":{"url":"http://127.0.0.1:8080","headers":{"Authorization":"Bearer test"}}}}`, func() {}, nil
		},
	})
	ctx := context.WithValue(context.Background(), "session_id", "child-session")
	inv, err := client.newMCPBridgeInvocation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer inv.revoke()
	joined := strings.Join(inv.args, "\n")
	if !strings.Contains(joined, "--plugin-dir") || strings.Contains(joined, "--approve-mcps") {
		t.Fatalf("isolated MCP args should load only the approved A2gent plugin: %v", inv.args)
	}
}

func containsString(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}

func environmentValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}
