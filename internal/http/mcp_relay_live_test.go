package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// Opt-in: runs Docker and the host's real Claude CLI, spending subscription quota.
func TestLiveDockerClaudeMCP(t *testing.T) {
	if os.Getenv("AAGENT_DOCKER_CLAUDE_MCP_LIVE") != "1" {
		t.Skip("set AAGENT_DOCKER_CLAUDE_MCP_LIVE=1 to exercise Docker -> host Claude -> child MCP")
	}
	t.Setenv("A2GENT_PARENT_PROXY_URL", "")
	t.Setenv(parentMCPKeyEnv, "")
	t.Setenv("AAGENT_CLAUDE_AGENT_SDK_SIDECAR_PATH", "")
	t.Setenv("AAGENT_CLAUDE_CLI_PERSISTENT", "false")
	defer claudecli.ClosePersistentProcesses()

	binary := filepath.Join(t.TempDir(), "relay-test")
	compileCtx, compileCancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer compileCancel()
	compile := exec.CommandContext(compileCtx, "go", "test", "-c", "-o", binary, ".")
	compile.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("cross-compile Linux child test: %v\n%s", err, out)
	}

	parent := newMCPBridgeTestServer(t)
	parent.config.HostClaudeCLI = true
	parent.config.WorkDir = t.TempDir()
	var parentCalls atomic.Int32
	parent.toolManager.Register(&bridgeDelegationProbe{name: "bridge_mcp_probe", execute: func(context.Context, json.RawMessage) (*tools.Result, error) {
		parentCalls.Add(1)
		t.Error("probe executed on parent instead of owning Docker child")
		return nil, fmt.Errorf("parent probe must never execute")
	}})
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewUnstartedServer(parent.router)
	_ = endpoint.Listener.Close()
	endpoint.Listener = listener
	endpoint.Start()
	defer endpoint.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	parent.port, err = strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := randomMCPRelaySecret()
	if err != nil {
		t.Fatal(err)
	}
	container := "t237-mcp-" + secret[:16]
	key := container + "." + secret
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "docker", "rm", "-f", container).CombinedOutput(); err != nil {
			t.Logf("Docker cleanup: %v: %s", err, out)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "run", "--name", container,
		"--platform", "linux/arm64",
		"--label", "a2gent.local_agent=true",
		"--label", mcpRelayKeyLabel+"="+mcpRelayKeyHash(key),
		"--add-host", "host.docker.internal:host-gateway",
		"--mount", "type=bind,src="+binary+",dst=/relay-test,readonly",
		"--entrypoint", "/relay-test",
		"-e", "T237_MCP_CHILD=1",
		"-e", "A2GENT_PARENT_PROXY_URL=http://host.docker.internal:"+port+"/v1",
		"-e", parentMCPKeyEnv+"="+key,
		"a2gent-brute:latest", "-test.run", "^TestDockerMCPRelayChild$", "-test.v", "-test.timeout=165s")
	out, err := cmd.CombinedOutput()
	t.Logf("Docker child output:\n%s", out)
	if err != nil {
		t.Fatalf("Docker Claude MCP round trip: %v (context: %v)", err, ctx.Err())
	}
	if !strings.Contains(string(out), "--- PASS: TestDockerMCPRelayChild") {
		t.Fatal("Docker child did not run and pass its probe assertions")
	}
	if parentCalls.Load() != 0 {
		t.Fatal("probe escaped to parent")
	}
	sessions, err := parent.sessionManager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("parent unexpectedly owns sessions: %d", len(sessions))
	}
}

// Executed only by the Linux test binary mounted into the real child container.
func TestDockerMCPRelayChild(t *testing.T) {
	if os.Getenv("T237_MCP_CHILD") != "1" {
		t.Skip("Docker child helper")
	}
	if path, err := exec.LookPath("claude"); err == nil {
		t.Fatalf("child must not have a Claude executable: %s", path)
	}
	if os.Getenv(parentMCPKeyEnv) == "" || os.Getenv("A2GENT_PARENT_PROXY_URL") == "" {
		t.Fatal("missing parent relay bootstrap environment")
	}
	child := newMCPBridgeTestServer(t)
	if child.parentMCPKey == "" || os.Getenv(parentMCPKeyEnv) != "" {
		t.Fatal("NewServer must consume and remove the parent MCP bootstrap key")
	}
	// Keep the project root equal to the default workdir so the registered test
	// probe survives toolManagerForSession rather than rebuilding another manager.
	projectID, workdir := "t237-child-project", child.config.WorkDir
	if workdir == "" {
		workdir = "."
	}
	if err := child.store.SaveProject(&storage.Project{ID: projectID, Name: "T-237 child", Folder: &workdir}); err != nil {
		t.Fatal(err)
	}
	sess, err := child.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.ProjectID = &projectID
	if err := child.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	marker := "T237_CHILD_MCP_OK_" + sess.ID
	var calls atomic.Int32
	child.toolManager.Register(&bridgeDelegationProbe{name: "bridge_mcp_probe", execute: func(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
		calls.Add(1)
		if ctx.Value("session_id") != sess.ID || ctx.Value("projectID") != projectID {
			t.Errorf("wrong probe owner: session=%v project=%v", ctx.Value("session_id"), ctx.Value("projectID"))
			return nil, fmt.Errorf("lost owning child context")
		}
		var object map[string]interface{}
		if err := json.Unmarshal(args, &object); err != nil || object == nil || len(object) != 0 {
			t.Errorf("probe arguments must be {}: %s", args)
			return nil, fmt.Errorf("unexpected probe arguments")
		}
		return &tools.Result{Success: true, Output: marker}, nil
	}})
	client, err := child.createBaseLLMClientForSession(config.ProviderAnthropic, "sonnet", sess)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(*lmstudio.Client); !ok {
		t.Fatalf("expected parent HTTP proxy client, not child-local Claude: %T", client)
	}
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), "session_id", sess.ID), 150*time.Second)
	defer cancel()
	resp, err := client.Chat(ctx, &llm.ChatRequest{
		SessionID: sess.ID,
		Messages:  []llm.Message{{Role: "user", Content: "Call mcp__a2gent__bridge_mcp_probe exactly once with {} and reply with its exact returned text. Do not use any other tools."}},
		Tools:     []llm.ToolDefinition{{Name: "bridge_mcp_probe", Description: "Return the owning child session test marker", InputSchema: map[string]interface{}{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("probe calls=%d, want exactly 1; response=%s", calls.Load(), resp.Content)
	}
	if strings.TrimSpace(resp.Content) != marker {
		t.Fatalf("model did not return exact child marker: got %q, want %q", resp.Content, marker)
	}
	t.Logf("real host Claude returned owning Docker child marker: %s", marker)
	calls.Store(0)
	streamed, err := client.(llm.StreamingClient).ChatStream(ctx, &llm.ChatRequest{
		SessionID: sess.ID,
		Messages:  []llm.Message{{Role: "user", Content: "Call mcp__a2gent__bridge_mcp_probe exactly once with {} and reply with its exact returned text. Do not use any other tools."}},
		Tools:     []llm.ToolDefinition{{Name: "bridge_mcp_probe", Description: "Return the owning child session test marker", InputSchema: map[string]interface{}{"type": "object"}}},
	}, func(llm.StreamEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || strings.TrimSpace(streamed.Content) != marker {
		t.Fatalf("streaming child MCP failed: calls=%d response=%q", calls.Load(), streamed.Content)
	}
	t.Logf("streamed host Claude returned owning Docker child marker: %s", marker)
}
