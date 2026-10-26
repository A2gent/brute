package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/storage"
)

// Reuse a fake warm host runtime, exercising the real direct-turn orchestration
// without launching another runtime, Docker, or an LLM.
func TestParentAgentToolsDirectTurnRevokes(t *testing.T) {
	for _, cancelTurn := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelTurn), func(t *testing.T) {
			s := newMCPBridgeTestServer(t)
			yaml := "version: \"1\"\nagent:\n  id: bridge-host\n  name: Bridge Host\nruntime:\n  type: host\nworkspace:\n  scope: none\n"
			now := time.Now()
			if err := s.store.SaveAgentDefinition(&storage.AgentDefinitionRecord{ID: "bridge-host", Name: "Bridge Host", Runtime: "host", DefinitionYAML: yaml, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			sess, _ := s.sessionManager.Create("build")
			sess.Metadata = map[string]interface{}{"unified_agent_id": "bridge-host"}
			if err := s.sessionManager.Save(sess); err != nil {
				t.Fatal(err)
			}
			def, err := s.unifiedAgentDefinitionForSession(sess)
			if err != nil {
				t.Fatal(err)
			}
			workspace, err := s.resolveDockerWorkspaceBinding(def, "")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var token string
			child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					w.WriteHeader(http.StatusOK)
				case "/sessions":
					var req CreateSessionRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					if req.Metadata["source"] != "direct_agent_session" || r.Header.Get(parentAgentToolsHeader) != "" {
						t.Error("unexpected child session creation")
					}
					_ = json.NewEncoder(w).Encode(CreateSessionResponse{ID: "direct-child"})
				case "/sessions/direct-child":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "direct-child", "status": "completed"})
				case "/sessions/direct-child/chat/stream":
					token = r.Header.Get(parentAgentToolsHeader)
					boundID, tokenCtx, ok := s.mcpBridge.resolve(token)
					if !ok || boundID != sess.ID || mcpBridgeToolExposedContext(tokenCtx, "question") {
						t.Error("missing restricted per-turn capability")
					}
					if cancelTurn {
						cancel()
						return
					}
					_ = json.NewEncoder(w).Encode(ChatStreamEvent{Type: "done", Content: "done", Status: "completed"})
				default:
					t.Errorf("unexpected child path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer child.Close()
			u, _ := url.Parse(child.URL)
			port, _ := strconv.Atoi(u.Port())
			name := containerNameForAgent(def.Agent.ID, workspace.ContainerNameBinding)
			s.hostRuntime.procs[name] = &hostAgentProc{
				name: name, defID: def.Agent.ID, port: port, done: make(chan struct{}),
				cmd: &exec.Cmd{
					Process: &os.Process{Pid: os.Getpid()},
					Env:     []string{parentAgentToolsURLEnv + "=http://127.0.0.1:" + strconv.Itoa(s.Port())},
				},
				labels: map[string]string{dockerRuntimeLLMFingerprintLabelKey: llmFingerprint(def)},
			}
			turns := 1
			if !cancelTurn {
				turns = 2
			}
			previousToken := ""
			for turn := 0; turn < turns; turn++ {
				_, err = s.runDirectAgentSession(ctx, sess, "work", nil)
				if (err != nil) != cancelTurn {
					t.Fatalf("turn error: %v", err)
				}
				if token == "" || token == previousToken {
					t.Fatal("host stream did not receive a fresh turn token")
				}
				previousToken = token
				s.mcpBridge.mu.Lock()
				remaining := len(s.mcpBridge.tokens)
				s.mcpBridge.mu.Unlock()
				if remaining != 0 {
					t.Fatal("completed/cancelled turn retained token")
				}
				stored, _ := s.sessionManager.Get(sess.ID)
				body, _ := json.Marshal(stored)
				if strings.Contains(string(body), token) {
					t.Fatal("token persisted in parent metadata")
				}
			}
		})
	}
}

func TestParentAgentToolsSpawnProvisionedWithoutProxy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(hostAgentHelperEnv, "1")
	s, _ := newUnifiedAgentsTestServer(t)
	settings, _ := s.store.GetSettings()
	settings[llmProviderProxyEnabledSettingKey] = "false"
	if err := s.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	def := &agentdef.Definition{
		Agent:     agentdef.AgentMeta{ID: "bridge-env-host", Name: "Bridge Env Host"},
		Runtime:   agentdef.Runtime{Type: agentdef.RuntimeHost},
		Workspace: agentdef.Workspace{Scope: agentdef.WorkspaceScopeNone},
	}
	workspace, err := s.resolveDockerWorkspaceBinding(def, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.hostRuntime.stopAll)
	proc, err := s.hostRuntime.spawn(t.Context(), def, "bridge-env-host", workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proc.stop)
	want := parentAgentToolsURLEnv + "=http://127.0.0.1:" + strconv.Itoa(s.Port())
	found := false
	for _, env := range proc.cmd.Env {
		if env == want {
			found = true
		}
		if strings.HasPrefix(env, parentAgentToolsURLEnv+"=") && strings.Contains(env, "token") {
			t.Fatal("token in parent URL env")
		}
	}
	if !found {
		t.Fatalf("missing spawn-provisioned parent origin %s", want)
	}
}

// Historical warm processes are real helper children, so stale bootstrap checks
// exercise stopping/reaping the old process and starting the current executable.
func TestParentAgentToolsWarmRuntimeBootstrap(t *testing.T) {
	for _, bootstrap := range []string{"missing", "wrong-port", "current"} {
		t.Run(bootstrap, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv(hostAgentHelperEnv, "1")
			s, _ := newUnifiedAgentsTestServer(t)
			s.port = 9876
			def := &agentdef.Definition{
				Agent:     agentdef.AgentMeta{ID: "bootstrap-host", Name: "Bootstrap Host"},
				Runtime:   agentdef.Runtime{Type: agentdef.RuntimeHost},
				Workspace: agentdef.Workspace{Scope: agentdef.WorkspaceScopeNone},
			}
			workspace, err := s.resolveDockerWorkspaceBinding(def, "")
			if err != nil {
				t.Fatal(err)
			}
			port, releasePort, err := reserveAvailableLocalDockerPort(t.Context(), defaultLocalAgentBasePort, defaultLocalAgentMaxPort)
			if err != nil {
				t.Fatal(err)
			}
			defer releasePort()
			cmd := exec.Command(os.Args[0], "server", "--port", strconv.Itoa(port))
			cmd.Env = mergedEnviron(nil)
			cmd.Env = slices.DeleteFunc(cmd.Env, func(env string) bool { return strings.HasPrefix(env, parentAgentToolsURLEnv+"=") })
			want := parentAgentToolsURLEnv + "=http://127.0.0.1:" + strconv.Itoa(s.Port())
			if bootstrap == "wrong-port" {
				cmd.Env = append(cmd.Env, parentAgentToolsURLEnv+"=http://127.0.0.1:9875")
			}
			if bootstrap == "current" {
				cmd.Env = append(cmd.Env, want)
			}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			name := containerNameForAgent(def.Agent.ID, workspace.ContainerNameBinding)
			old := &hostAgentProc{name: name, defID: def.Agent.ID, port: port, cmd: cmd, done: make(chan struct{}), labels: map[string]string{dockerRuntimeLLMFingerprintLabelKey: llmFingerprint(def)}}
			go func() { _ = cmd.Wait(); close(old.done) }()
			t.Cleanup(old.stop)
			s.hostRuntime.procs[name] = old
			t.Cleanup(s.hostRuntime.stopAll)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if err := waitForLocalDockerAgentHTTP(ctx, &http.Client{Timeout: time.Second}, old.asAgent().APIURL); err != nil {
				t.Fatal(err)
			}
			if _, err := s.hostRuntime.ensure(ctx, def, workspace); err != nil {
				t.Fatal(err)
			}
			current := s.hostRuntime.procs[name]
			if bootstrap == "current" {
				if current != old || !old.alive() {
					t.Fatal("correct bootstrap did not reuse warm child")
				}
			} else {
				if current == old || old.alive() {
					t.Fatal("stale bootstrap did not stop and replace warm child")
				}
				if current.cmd.Process.Pid == old.cmd.Process.Pid || !slices.Contains(current.cmd.Env, want) {
					t.Fatal("replacement lacks current parent bootstrap")
				}
			}
			if _, err := s.hostRuntime.ensure(ctx, def, workspace); err != nil || s.hostRuntime.procs[name] != current {
				t.Fatalf("successive ensure failed to reuse current bootstrap: %v", err)
			}
		})
	}
}
