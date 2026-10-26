package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/cursorcli"
)

// This gate must precede even auth discovery: ordinary go test must never use
// the developer's Cursor account or start Docker for this test.
func TestLiveDockerCursorMCP(t *testing.T) {
	if os.Getenv("AAGENT_DOCKER_CURSOR_MCP_LIVE") != "1" {
		t.Skip("set AAGENT_DOCKER_CURSOR_MCP_LIVE=1 to spend Cursor quota on Docker -> host Cursor -> child MCP")
	}
	if os.Getenv("CURSOR_API_KEY") == "" {
		if _, err := cursorcli.ResolveAccessToken(); err != nil {
			t.Skip("Cursor credentials unavailable; run agent login or set CURSOR_API_KEY")
		}
	}
	runLiveDockerMCP(t, config.ProviderCursor)
}

func prepareLiveCursorRelayAudit(t *testing.T, parentWorkDir string) {
	t.Helper()
	realCLI := strings.TrimSpace(os.Getenv("AAGENT_CURSOR_CLI_PATH"))
	if realCLI == "" {
		realCLI = "agent"
	}
	realCLI, err := exec.LookPath(realCLI)
	if err != nil {
		t.Fatalf("Cursor CLI unavailable: %v", err)
	}
	realCLI, err = filepath.Abs(realCLI)
	if err != nil {
		t.Fatal(err)
	}
	auditDir := t.TempDir()
	parentConfig := t.TempDir()
	home := t.TempDir()
	// Keep authentication explicit while placing user-level MCP/chats in a
	// disposable home. Never copy or alter the developer's real MCP/chat data.
	if os.Getenv("CURSOR_API_KEY") == "" {
		token, err := cursorcli.ResolveAccessToken()
		if err != nil {
			t.Fatal(err)
		}
		auth, _ := json.Marshal(map[string]string{"accessToken": token})
		for _, dir := range []string{".cursor", ".cursor-agent", ".config/cursor", ".config/cursor-agent"} {
			path := filepath.Join(home, dir)
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "auth.json"), auth, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	var unrelatedCalls atomic.Int32
	unrelated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		unrelatedCalls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(unrelated.Close)
	// Seed an unrelated parent project/config server without editing the user's
	// real ~/.cursor files. Any connection to this server is an isolation failure.
	mcpJSON, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"unrelated-parent-canary": map[string]string{"url": unrelated.URL},
	}})
	projectCursor := filepath.Join(parentWorkDir, ".cursor")
	if err := os.MkdirAll(projectCursor, 0o700); err != nil {
		t.Fatal(err)
	}
	userCursor := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(userCursor, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{projectCursor, parentConfig, userCursor} {
		if err := os.WriteFile(filepath.Join(dir, "mcp.json"), mcpJSON, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A quota-free control prevents a misspelled/ignored canary configuration
	// from making the live isolation assertion vacuous.
	controlCtx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	control := exec.CommandContext(controlCtx, realCLI, "--workspace", parentWorkDir, "mcp", "list")
	control.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"CURSOR_CONFIG_DIR="+parentConfig, "AGENT_CLI_CREDENTIAL_STORE=file")
	out, err := control.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "unrelated-parent-canary") {
		t.Fatalf("Cursor MCP canary control failed: %v", err)
	}
	unrelatedCalls.Store(0)

	// Snapshot the actual subprocess boundary, then exec (not mock) Cursor.
	// exec preserves cancellation and stdout streaming to the production client.
	script := `#!/bin/sh
set -eu
umask 077
format='' plugin='' previous=''
for arg in "$@"; do
  case "$previous" in
    --output-format) format="$arg" ;;
    --plugin-dir) plugin="$arg" ;;
  esac
  previous="$arg"
done
case "$format" in json|stream-json) ;; *) exit 91 ;; esac
snapshot="$AAGENT_CURSOR_RELAY_AUDIT/$format"
mkdir "$snapshot"
printf '%s\0' "$@" > "$snapshot/args"
printf '%s\0%s\0' "$PWD" "$CURSOR_CONFIG_DIR" > "$snapshot/env"
cp .cursor/cli.json "$snapshot/project.json"
cp .cursor/sandbox.json "$snapshot/sandbox.json"
cp "$CURSOR_CONFIG_DIR/cli-config.json" "$snapshot/global.json"
cp "$plugin/.cursor-plugin/plugin.json" "$snapshot/plugin.json"
export HOME="$AAGENT_CURSOR_RELAY_HOME"
export XDG_CONFIG_HOME="$HOME/.config"
export AGENT_CLI_CREDENTIAL_STORE=file
exec "$AAGENT_CURSOR_RELAY_REAL_CLI" "$@"
`
	wrapper := filepath.Join(auditDir, "agent")
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AAGENT_CURSOR_CLI_PATH", wrapper)
	t.Setenv("AAGENT_CURSOR_RELAY_REAL_CLI", realCLI)
	t.Setenv("AAGENT_CURSOR_RELAY_AUDIT", auditDir)
	t.Setenv("AAGENT_CURSOR_RELAY_HOME", home)
	t.Setenv("CURSOR_CONFIG_DIR", parentConfig)
	t.Cleanup(func() {
		if unrelatedCalls.Load() != 0 {
			t.Errorf("Cursor contacted unrelated parent MCP server %d times", unrelatedCalls.Load())
		}
		workspaces := map[string]bool{}
		for _, format := range []string{"json", "stream-json"} {
			workspace := assertLiveCursorRelaySnapshot(t, filepath.Join(auditDir, format), parentWorkDir, parentConfig)
			if workspaces[workspace] {
				t.Error("Cursor reused a previous relay workspace/chat scope")
			}
			workspaces[workspace] = true
		}
	})
}

func assertLiveCursorRelaySnapshot(t *testing.T, dir, parentWorkDir, parentConfig string) string {
	t.Helper()
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("missing Cursor subprocess audit %s: %v", name, err)
		}
		return data
	}
	args := strings.Split(strings.TrimSuffix(string(read("args")), "\x00"), "\x00")
	value := func(flag string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == flag {
				return args[i+1]
			}
		}
		return ""
	}
	for _, arg := range args {
		for _, forbidden := range []string{"--resume", "--continue", "--force", "--approve-mcps", "-f"} {
			if arg == forbidden || strings.HasPrefix(arg, forbidden+"=") {
				t.Errorf("Cursor relay passed forbidden flag %s", forbidden)
			}
		}
	}
	workspace, plugin := value("--workspace"), value("--plugin-dir")
	if workspace == "" || workspace == parentWorkDir || plugin == "" || value("--sandbox") != "enabled" {
		t.Fatal("Cursor did not use an isolated workspace, sandbox and MCP plugin")
	}
	env := strings.Split(string(read("env")), "\x00")
	physicalWorkspace, err := filepath.EvalSymlinks(filepath.Dir(workspace))
	if err != nil {
		t.Fatal(err)
	}
	physicalWorkspace = filepath.Join(physicalWorkspace, filepath.Base(workspace))
	if len(env) != 3 || env[0] != physicalWorkspace || env[1] == parentConfig || env[1] != filepath.Join(workspace, ".cursor-cli-config") {
		t.Fatal("Cursor inherited parent cwd/configuration")
	}
	for _, name := range []string{"project.json", "global.json"} {
		var conf struct {
			ApprovalMode string                         `json:"approvalMode"`
			Permissions  struct{ Allow, Deny []string } `json:"permissions"`
		}
		if err := json.Unmarshal(read(name), &conf); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(conf.Permissions.Allow, []string{"Mcp(plugin-a2gent-bridge-a2gent:*)"}) {
			t.Errorf("%s must allow only the child A2gent MCP plugin", name)
		}
		wantDeny := []string{"Shell(*)", "Read(**)", "Read(/**)", "Read(~/**)", "Write(**)", "Write(/**)", "Write(~/**)", "WebFetch(*)"}
		if !reflect.DeepEqual(conf.Permissions.Deny, wantDeny) || (name == "global.json" && conf.ApprovalMode != "allowlist") {
			t.Errorf("%s does not deny parent shell/filesystem/web tools", name)
		}
	}
	var manifest struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(read("plugin.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.MCPServers) != 1 || manifest.MCPServers["a2gent"] == nil {
		t.Error("Cursor plugin exposes unrelated MCP servers")
	}
	var sandbox struct {
		Type            string `json:"type"`
		ReadBoundary    string `json:"readBoundary"`
		DisableTmpWrite bool   `json:"disableTmpWrite"`
	}
	if err := json.Unmarshal(read("sandbox.json"), &sandbox); err != nil {
		t.Fatal(err)
	}
	if sandbox.Type != "workspace_readwrite" || sandbox.ReadBoundary != "workspace" || !sandbox.DisableTmpWrite {
		t.Error("Cursor sandbox does not confine parent filesystem access")
	}
	for _, path := range []string{workspace, plugin} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("temporary Cursor workspace/plugin not removed: %s (%v)", path, err)
		}
	}
	return workspace
}
