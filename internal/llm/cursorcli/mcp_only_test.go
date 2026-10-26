package cursorcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	if strings.Contains(string(data), `"version"`) || len(cliConfig.Permissions.Allow) != 1 || cliConfig.Permissions.Allow[0] != "Mcp(plugin-a2gent-bridge-a2gent:*)" {
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

func TestMCPOnlySubprocessInvocation(t *testing.T) {
	for _, method := range []string{"Chat", "ChatStream"} {
		for _, cursorWorkspace := range []string{"host", "isolated"} {
			t.Run(method+"/"+cursorWorkspace+"Cursor", func(t *testing.T) {
				registerTestChatDir(t, testChatID)
				client, argsFile := newFakeCursorClient(t, t.TempDir(), false)
				workDir := t.TempDir()
				captureDir := t.TempDir()
				t.Setenv("CAPTURE_DIR", captureDir)
				t.Setenv("CURSOR_CONFIG_DIR", t.TempDir())
				client.options.Force = true
				client.options.Trust = false
				client.options.Sandbox = "disabled"
				bridgeCalls, revokes := 0, 0
				client.options.MCPBridge = func(_ context.Context, sessionID string) (string, func(), error) {
					bridgeCalls++
					if sessionID != "child-session" {
						t.Errorf("bridge session = %q, want child-session", sessionID)
					}
					return `{"mcpServers":{"a2gent":{"url":"http://127.0.0.1:8080","headers":{"Authorization":"Bearer test"}}}}`, func() { revokes++ }, nil
				}
				originalOptions := client.options

				request := resumeRequest(client)
				request.ProviderSessionCursor = client.sessionCursor(client.model, testChatID)
				control := client
				if cursorWorkspace == "isolated" {
					// A workspace mismatch alone must not mask a missing MCPOnly resume guard.
					options := client.options
					options.WorkDir = workDir
					control = NewClientWithOptions(client.model, options)
					request.ProviderSessionCursor = control.sessionCursor(client.model, testChatID)
				}
				if got := control.resumeChatID(client.model, request.ProviderSessionCursor); got != testChatID {
					t.Fatalf("non-MCPOnly control resumeChatID = %q, want %q", got, testChatID)
				}

				// Snapshot files in the subprocess, not after it: configuration must
				// already confine native tools when Cursor starts.
				script, err := os.ReadFile(client.options.Executable)
				if err != nil {
					t.Fatal(err)
				}
				capture := `#!/bin/sh
set -eu
printf '%s\0%s\0' "$PWD" "$CURSOR_CONFIG_DIR" > "$CAPTURE_DIR/env"
cp .cursor/cli.json "$CAPTURE_DIR/project.json"
cp .cursor/sandbox.json "$CAPTURE_DIR/sandbox.json"
cp "$CURSOR_CONFIG_DIR/cli-config.json" "$CAPTURE_DIR/global.json"
previous=''
for arg in "$@"; do
  if [ "$previous" = '--plugin-dir' ]; then
    cp "$arg/.cursor-plugin/plugin.json" "$CAPTURE_DIR/plugin.json"
  fi
  previous="$arg"
done
`
				if err := os.WriteFile(client.options.Executable, []byte(strings.Replace(string(script), "#!/bin/sh\n", capture, 1)), 0o755); err != nil {
					t.Fatal(err)
				}

				ctx := WithMCPOnly(context.WithValue(t.Context(), "session_id", "child-session"), workDir)
				var response *llm.ChatResponse
				if method == "Chat" {
					response, err = client.Chat(ctx, request)
				} else {
					response, err = client.ChatStream(ctx, request, nil)
				}
				if err != nil {
					t.Fatalf("%s: %v", method, err)
				}
				if response.Content != "answer" {
					t.Fatalf("response = %+v, want fake answer", response)
				}
				if bridgeCalls != 1 || revokes != 1 {
					t.Errorf("bridge calls/revokes = %d/%d, want 1/1", bridgeCalls, revokes)
				}
				calls := readCalls(t, argsFile)
				if len(calls) != 1 {
					t.Fatalf("subprocess calls = %d, want 1", len(calls))
				}
				args := calls[0]
				for _, forbidden := range []string{"--resume", "--continue", "--force", "--approve-mcps"} {
					if containsString(args, forbidden) {
						t.Errorf("unexpected %s in args: %v", forbidden, args)
					}
				}
				format := "json"
				if method == "ChatStream" {
					format = "stream-json"
				}
				for flag, value := range map[string]string{"--workspace": workDir, "--sandbox": "enabled", "--output-format": format} {
					if !containsPair(args, flag, value) {
						t.Errorf("missing %s %s in args: %v", flag, value, args)
					}
				}
				for _, want := range []string{"first question", "first answer", "second question", cursorMCPOnlyPrompt} {
					if !strings.Contains(promptArg(args), want) {
						t.Errorf("fresh isolated prompt missing %q", want)
					}
				}
				pluginDir := ""
				for i := 0; i+1 < len(args); i++ {
					if args[i] == "--plugin-dir" {
						pluginDir = args[i+1]
					}
				}
				if pluginDir == "" {
					t.Fatal("missing temporary MCP plugin")
				}
				if _, err := os.Stat(pluginDir); !os.IsNotExist(err) {
					t.Errorf("temporary plugin still exists: %q, err = %v", pluginDir, err)
				}
				env, err := os.ReadFile(filepath.Join(captureDir, "env"))
				if err != nil {
					t.Fatal(err)
				}
				// Resolve macOS /var -> /private/var for the shell's physical cwd.
				cwd, err := filepath.EvalSymlinks(workDir)
				if err != nil {
					t.Fatal(err)
				}
				if got, want := string(env), cwd+"\x00"+filepath.Join(workDir, ".cursor-cli-config")+"\x00"; got != want {
					t.Errorf("subprocess cwd/config = %q, want %q", got, want)
				}
				for _, name := range []string{"project.json", "global.json"} {
					data, err := os.ReadFile(filepath.Join(captureDir, name))
					if err != nil {
						t.Fatal(err)
					}
					var config cursorCLIConfig
					if err := json.Unmarshal(data, &config); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(config.Permissions.Allow, []string{"Mcp(plugin-a2gent-bridge-a2gent:*)"}) {
						t.Errorf("%s allow = %v", name, config.Permissions.Allow)
					}
					for _, deny := range []string{"Shell(*)", "Read(/**)", "Write(/**)", "WebFetch(*)"} {
						if !containsString(config.Permissions.Deny, deny) {
							t.Errorf("%s missing deny %q", name, deny)
						}
					}
				}
				if reflect.ValueOf(client.options.MCPBridge).Pointer() != reflect.ValueOf(originalOptions.MCPBridge).Pointer() {
					t.Error("original MCP bridge changed")
				}
				gotOptions := client.options
				gotOptions.MCPBridge, originalOptions.MCPBridge = nil, nil
				if !reflect.DeepEqual(gotOptions, originalOptions) || client.model != "composer-2.5" {
					t.Errorf("original client changed: %+v", client)
				}
			})
		}
	}
}

// registerTestChatDir keeps resume checks independent of real Cursor chats.
func registerTestChatDir(t *testing.T, chatID string) {
	t.Helper()
	createChat(t, useTempChatsDir(t), chatID)
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
