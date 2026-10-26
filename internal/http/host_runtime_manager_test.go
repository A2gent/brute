package http

import (
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/storage"
)

const hostAgentHelperEnv = "A2GENT_TEST_HOST_AGENT_HELPER"

// runHostAgentHelperIfRequested turns the test binary into a stand-in for
// `brute server --port N`: it serves /health and /env and never returns.
func runHostAgentHelperIfRequested() {
	if os.Getenv(hostAgentHelperEnv) != "1" {
		return
	}
	var port string
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	mux := nethttp.NewServeMux()
	mux.HandleFunc("/health", func(w nethttp.ResponseWriter, _ *nethttp.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/env", func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		cwd, _ := os.Getwd()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"prompt": os.Getenv("AAGENT_SYSTEM_PROMPT"),
			"data":   os.Getenv("AAGENT_DATA_PATH"),
			"home":   os.Getenv("HOME"),
			"cwd":    cwd,
		})
	})
	_ = nethttp.ListenAndServe("127.0.0.1:"+port, mux)
	os.Exit(1)
}

func TestHostEnvFromDockerArgsKeepsOnlyEnv(t *testing.T) {
	env := hostEnvFromDockerArgs([]string{"--volume", "a:b", "--env", "K=v=1", "--label", "x=y", "--env", "EMPTY="})
	if len(env) != 2 || env["K"] != "v=1" || env["EMPTY"] != "" {
		t.Fatalf("unexpected env: %#v", env)
	}
}

func TestMergedEnvironOverridesHostValues(t *testing.T) {
	t.Setenv("A2GENT_TEST_KEEP", "keep")
	t.Setenv("A2GENT_TEST_OVERRIDE", "old")
	out := strings.Join(mergedEnviron(map[string]string{"A2GENT_TEST_OVERRIDE": "new"}), "\n")
	if !strings.Contains(out, "A2GENT_TEST_KEEP=keep") || !strings.Contains(out, "A2GENT_TEST_OVERRIDE=new") || strings.Contains(out, "A2GENT_TEST_OVERRIDE=old") {
		t.Fatalf("host env not merged correctly:\n%s", out)
	}
}

func TestHostRuntimeSpawnsReusesAndStopsProcess(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // agent data dir lives under HOME
	t.Setenv(hostAgentHelperEnv, "1")
	server, store := newUnifiedAgentsTestServer(t)
	projectRoot := t.TempDir()
	if err := store.SaveProject(&storage.Project{ID: "godot-game", Name: "Game", Folder: &projectRoot, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	def := &agentdef.Definition{
		Agent:     agentdef.AgentMeta{ID: "godot-dev", Name: "Godot Dev"},
		Runtime:   agentdef.Runtime{Type: agentdef.RuntimeHost},
		Workspace: agentdef.Workspace{Scope: agentdef.WorkspaceScopeCurrentProject},
	}
	workspace, err := server.resolveDockerWorkspaceBinding(def, "godot-game")
	if err != nil {
		t.Fatal(err)
	}
	if !workspace.Host {
		t.Fatal("host definition must produce a host workspace binding")
	}
	t.Cleanup(server.hostRuntime.stopAll)

	agent, err := server.dockerRuntime.ensureAgentContainerForWorkspace(t.Context(), def, workspace)
	if err != nil {
		t.Fatalf("ensure host agent: %v", err)
	}
	if agent.Runtime != agentdef.RuntimeHost || !agent.Running || agent.Labels[dockerRuntimeAgentDefLabelKey] != "godot-dev" {
		t.Fatalf("unexpected agent: %+v", agent)
	}

	resp, err := nethttp.Get(agent.APIURL + "/env")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got["cwd"], projectRoot) && got["cwd"] != projectRoot {
		// macOS resolves /var -> /private/var, so compare by suffix as well.
		t.Fatalf("agent must run in the real project folder, got cwd %q want %q", got["cwd"], projectRoot)
	}
	if !strings.Contains(got["data"], "local-agents") || got["home"] == got["data"] {
		t.Fatalf("agent needs its own data dir but the host HOME: %#v", got)
	}
	if strings.Contains(got["prompt"], "/workspace") || !strings.Contains(got["prompt"], projectRoot) {
		t.Fatalf("host prompt must keep real paths:\n%s", got["prompt"])
	}

	again, err := server.dockerRuntime.ensureAgentContainerForWorkspace(t.Context(), def, workspace)
	if err != nil || again.APIURL != agent.APIURL {
		t.Fatalf("second ensure should reuse the warm process: %v %+v", err, again)
	}
	if listed := server.hostRuntime.list(); len(listed) != 1 {
		t.Fatalf("expected one listed host agent, got %d", len(listed))
	}

	if stopped := server.hostRuntime.stopForDefinition("godot-dev", ""); len(stopped) != 1 {
		t.Fatalf("expected the process to be stopped, got %v", stopped)
	}
	if _, err := nethttp.Get(fmt.Sprintf("%s/health", agent.APIURL)); err == nil {
		t.Fatal("process should be gone after stop")
	}
	if listed := server.hostRuntime.list(); len(listed) != 0 {
		t.Fatalf("stopped host process must not be reused as live: %+v", listed)
	}
	restarted, err := server.dockerRuntime.ensureAgentContainerForWorkspace(t.Context(), def, workspace)
	if err != nil || restarted.Runtime != agentdef.RuntimeHost || !restarted.Running {
		t.Fatalf("next delegation must restart the stopped host process: %+v %v", restarted, err)
	}
}

func TestHostRuntimeRejectsMultiProjectScope(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	def := &agentdef.Definition{Agent: agentdef.AgentMeta{ID: "multi"}, Runtime: agentdef.Runtime{Type: agentdef.RuntimeHost}}
	_, err := server.hostRuntime.ensure(t.Context(), def, dockerWorkspaceBinding{ProjectMounts: []dockerWorkspaceProjectMount{{ProjectID: "a"}}})
	if err == nil {
		t.Fatal("multi-project workspaces must be rejected for host agents")
	}
}

func TestRewriteDelegationTaskSkipsHostWorkspace(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	task := "open /Users/me/game/project.godot"
	if got := server.rewriteDockerDelegationTask(task, dockerWorkspaceBinding{Host: true, ProjectID: "p"}); got != task {
		t.Fatalf("host delegation task must be untouched, got %q", got)
	}
}
