// host_runtime_manager.go runs runtime.type=host agent definitions as child
// Brute server processes directly on the host (no Docker). It exists for work
// that needs native host tooling, e.g. driving Godot. Each process is exposed
// to the rest of the code as a LocalDockerAgent so delegation, direct sessions
// and child-session proxying reuse the Docker flow unchanged.
package http

import (
	"context"
	"fmt"
	nethttp "net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/storage"
)

const hostRuntimeStopTimeout = 5 * time.Second

type hostAgentProc struct {
	name      string
	defID     string
	projectID string
	port      int
	cmd       *exec.Cmd
	done      chan struct{} // closed when the process exits
	labels    map[string]string
}

func (p *hostAgentProc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *hostAgentProc) asAgent() *LocalDockerAgent {
	return &LocalDockerAgent{
		ID:       p.name,
		Name:     p.name,
		Image:    "host",
		State:    "running",
		Status:   "running on host (pid " + fmt.Sprint(p.cmd.Process.Pid) + ")",
		Runtime:  agentdef.RuntimeHost,
		Labels:   p.labels,
		Managed:  true,
		Running:  true,
		HostPort: p.port,
		APIURL:   fmt.Sprintf("http://127.0.0.1:%d", p.port),
	}
}

type hostRuntimeManager struct {
	server *Server

	mu       sync.Mutex
	procs    map[string]*hostAgentProc
	creating map[string]*sync.Mutex
}

func newHostRuntimeManager(server *Server) *hostRuntimeManager {
	return &hostRuntimeManager{
		server:   server,
		procs:    make(map[string]*hostAgentProc),
		creating: make(map[string]*sync.Mutex),
	}
}

func (m *hostRuntimeManager) creationLock(name string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.creating[name]
	if !ok {
		lock = &sync.Mutex{}
		m.creating[name] = lock
	}
	return lock
}

// list returns live host agents shaped like managed containers, so agent
// listings and system-prompt discovery treat them like running Docker agents.
func (m *hostRuntimeManager) list() []LocalDockerAgent {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	agents := make([]LocalDockerAgent, 0, len(m.procs))
	for _, p := range m.procs {
		if p.alive() {
			agents = append(agents, *p.asAgent())
		}
	}
	return agents
}

// stopForDefinition stops every host process of a definition except exceptName.
func (m *hostRuntimeManager) stopForDefinition(defID string, exceptName string) []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	var victims []*hostAgentProc
	for name, p := range m.procs {
		if p.defID == defID && name != exceptName {
			victims = append(victims, p)
			delete(m.procs, name)
		}
	}
	m.mu.Unlock()
	stopped := make([]string, 0, len(victims))
	for _, p := range victims {
		p.stop()
		stopped = append(stopped, p.name)
	}
	return stopped
}

// stopByName stops one host process; false means no such host agent.
func (m *hostRuntimeManager) stopByName(name string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	p := m.procs[name]
	delete(m.procs, name)
	m.mu.Unlock()
	if p == nil {
		return false
	}
	p.stop()
	return true
}

// logPath is where a host agent's stdout/stderr go ("docker logs" equivalent).
func hostAgentLogPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".a2gent-data", "local-agents", name, "agent.log"), nil
}

func (m *hostRuntimeManager) stopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	victims := make([]*hostAgentProc, 0, len(m.procs))
	for name, p := range m.procs {
		victims = append(victims, p)
		delete(m.procs, name)
	}
	m.mu.Unlock()
	// In parallel: the parent is exiting and each stop may wait out its timeout.
	var wg sync.WaitGroup
	for _, p := range victims {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.stop()
		}()
	}
	wg.Wait()
}

func (p *hostAgentProc) stop() {
	if !p.alive() {
		return
	}
	// Signal the whole group: host tools started by the agent (godot, etc.) die with it.
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(hostRuntimeStopTimeout):
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	}
}

// ensure returns a healthy host process for the definition, spawning one when
// needed. Like the Docker runtime there is one instance per definition.
func (m *hostRuntimeManager) ensure(ctx context.Context, def *agentdef.Definition, workspace dockerWorkspaceBinding) (*LocalDockerAgent, error) {
	if def == nil || def.Runtime.Type != agentdef.RuntimeHost {
		return nil, fmt.Errorf("definition is not a host runtime agent")
	}
	if len(workspace.ProjectMounts) > 0 {
		return nil, fmt.Errorf("host runtime supports a single project workspace; use none, current_project or configured_project scope")
	}
	name := containerNameForAgent(def.Agent.ID, workspace.ContainerNameBinding)
	lock := m.creationLock(name)
	lock.Lock()
	defer lock.Unlock()

	m.stopForDefinition(def.Agent.ID, name)

	m.mu.Lock()
	existing := m.procs[name]
	m.mu.Unlock()
	// Warm children started before the parent-tools bootstrap (or on another
	// parent port) must respawn from the current executable before accepting turns.
	parentToolsEnv := fmt.Sprintf("%s=http://127.0.0.1:%d", parentAgentToolsURLEnv, m.server.Port())
	if existing != nil && existing.alive() && !slices.Contains(existing.cmd.Env, parentToolsEnv) {
		logging.Info("Host runtime: agent %s has outdated parent tools bootstrap; restarting", name)
		existing.stop()
		existing = nil
	}
	if existing != nil && existing.alive() && llmConfigStale(existing.labels, def) {
		logging.Info("Host runtime: agent %s runs an outdated LLM config; restarting", name)
		existing.stop()
		existing = nil
	}
	if existing != nil && existing.alive() {
		healthCtx, cancel := context.WithTimeout(ctx, dockerRuntimeHealthTimeout)
		err := waitForLocalDockerAgentHTTP(healthCtx, &nethttp.Client{Timeout: dockerRuntimeHealthTimeout}, existing.asAgent().APIURL)
		cancel()
		if err == nil {
			return existing.asAgent(), nil
		}
		logging.Warn("Host runtime: agent %s is unhealthy (%v); restarting", name, err)
		existing.stop()
	}

	p, err := m.spawn(ctx, def, name, workspace)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.procs[name] = p
	m.mu.Unlock()
	return p.asAgent(), nil
}

func (m *hostRuntimeManager) spawn(ctx context.Context, def *agentdef.Definition, name string, workspace dockerWorkspaceBinding) (*hostAgentProc, error) {
	s := m.server
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot locate brute executable: %w", err)
	}
	// Separate data dir and config keep the child's DB, sessions and integrations
	// (e.g. Telegram polling) away from the parent, as a container's /data does.
	logPath, err := hostAgentLogPath(name)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve home directory: %w", err)
	}
	dataDir := filepath.Dir(logPath)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to prepare agent data directory: %w", err)
	}
	reapHostAgentPIDFile(dataDir) // a previous parent's instance would clash on data and Telegram polling

	workDir := ""
	if workspace.ProjectID != "" {
		if workDir, err = s.resolveProjectRootFolder(workspace.ProjectID); err != nil {
			return nil, fmt.Errorf("invalid project: %w", err)
		}
	}

	req := localDockerCreateRequestBaseFromDefinition(def)
	prompt, err := s.composeAgentSystemPrompt(def, workspace.ProjectID, true)
	if err != nil {
		return nil, err
	}
	if workDir != "" {
		prompt = strings.TrimSpace(prompt + "\n\nHost workspace note: you run directly on the host (not in Docker); the project root is " + workDir + ".")
	}

	useProxy := s.llmProxyEnabled() && !localDockerAgentBypassesParentLLMProxy(req)
	if useProxy {
		req.LLM = s.resolveLocalDockerAgentProxyLLM(req)
	}
	var args []string
	if args, err = s.applyLocalDockerAgentToolsArgs(args, req.Tools); err != nil {
		return nil, err
	}
	if args, err = appendLocalDockerAgentExtraArgs(args, req, ""); err != nil {
		return nil, err
	}
	if useProxy {
		args = append(args, s.parentProxyEnvArgs(localDockerAgentRuntimeProvider(req.LLM.Provider), fmt.Sprintf("http://127.0.0.1:%d/v1", s.Port()))...)
	}
	env := hostEnvFromDockerArgs(args)
	// Origin only: turn credentials travel in an internal stream header, never env.
	env[parentAgentToolsURLEnv] = fmt.Sprintf("http://127.0.0.1:%d", s.Port())
	env[hostParentPIDEnv] = fmt.Sprint(os.Getpid())
	env["AAGENT_DATA_PATH"] = dataDir
	env["AAGENT_CONFIG_PATH"] = filepath.Join(dataDir, "config.json")
	env[storage.ChildRuntimeEnv] = "1"
	if prompt != "" {
		env["AAGENT_SYSTEM_PROMPT"] = prompt
	}
	if kind := strings.TrimSpace(def.Agent.Kind); kind != "" {
		env["A2GENT_AGENT_KIND"] = kind
	}
	if defDir := strings.TrimSpace(def.Local.DefinitionDir); defDir != "" {
		env["A2GENT_AGENT_DEFINITION_DIR"] = defDir
		env["AAGENT_AGENT_DEFINITION_DIR"] = defDir
		if skillsDir := filepath.Join(defDir, "skills"); directoryExists(skillsDir) {
			env[skillsFolderSettingKey] = skillsDir
		}
	}

	port, releasePort, err := reserveAvailableLocalDockerPort(ctx, defaultLocalAgentBasePort, defaultLocalAgentMaxPort)
	if err != nil {
		return nil, fmt.Errorf("no available host port found in local agent range")
	}
	defer releasePort()

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open agent log: %w", err)
	}
	// Not CommandContext: the process must outlive the delegation request that spawned it.
	cmd := exec.Command(exe, "server", "--port", fmt.Sprint(port))
	cmd.Dir = workDir
	cmd.Env = mergedEnviron(env)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logging.Info("Host runtime: starting agent %s (def=%s project=%s port=%d dir=%s)", name, def.Agent.ID, workspace.ProjectID, port, workDir)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("failed to start host agent %s: %w", name, err)
	}
	pid := cmd.Process.Pid
	writeHostAgentPIDFile(dataDir, hostAgentPIDRecord{PID: pid, Port: port, Exe: exe})

	labels := map[string]string{
		dockerRuntimeManagedLabelKey:        "true",
		dockerRuntimeAgentDefLabelKey:       def.Agent.ID,
		dockerRuntimeLLMFingerprintLabelKey: llmFingerprint(def),
	}
	if workspace.ProjectID != "" {
		labels["a2gent.project_id"] = workspace.ProjectID
	}
	if provider, model := dockerDelegationLLMMetadataFromDefinition(def); provider != "" {
		labels[dockerRuntimeLLMProviderLabelKey] = provider
		if model != "" {
			labels[dockerRuntimeLLMModelLabelKey] = model
		}
	}
	if useProxy {
		labels[dockerRuntimeLLMProviderLabelKey] = req.LLM.Provider
		if req.LLM.Model != "" {
			labels[dockerRuntimeLLMModelLabelKey] = req.LLM.Model
		}
	}
	p := &hostAgentProc{name: name, defID: def.Agent.ID, projectID: workspace.ProjectID, port: port, cmd: cmd, done: make(chan struct{}), labels: labels}
	go func() {
		_ = cmd.Wait()
		logFile.Close()
		removeHostAgentPIDFile(dataDir, pid)
		close(p.done)
		logging.Info("Host runtime: agent %s exited", name)
	}()

	healthCtx, cancel := context.WithTimeout(ctx, dockerRuntimeHealthTimeout)
	defer cancel()
	if err := waitForLocalDockerAgentHTTP(healthCtx, &nethttp.Client{Timeout: dockerRuntimeHealthTimeout}, p.asAgent().APIURL); err != nil {
		p.stop()
		return nil, fmt.Errorf("host agent %s is unhealthy (see %s): %w", name, logPath, err)
	}
	return p, nil
}

// hostEnvFromDockerArgs extracts the "--env K=V" pairs from docker run args;
// every other flag (volumes, labels, network, limits) is container-only.
func hostEnvFromDockerArgs(args []string) map[string]string {
	env := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "--env" {
			continue
		}
		if key, value, ok := strings.Cut(args[i+1], "="); ok && key != "" {
			env[key] = value
		}
		i++
	}
	return env
}

func hasKey(m map[string]string, key string) bool {
	_, ok := m[key]
	return ok
}

// mergedEnviron overlays overrides on the host environment so the agent keeps
// PATH, HOME and tool settings (godot, etc.).
func mergedEnviron(overrides map[string]string) []string {
	out := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		if key, _, _ := strings.Cut(kv, "="); !hasKey(overrides, key) {
			out = append(out, kv)
		}
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}

// hostAgentLogs returns the last lines of a running host agent's log.
func (s *Server) hostAgentLogs(name string, tail int) (string, bool) {
	known := false
	for _, a := range s.hostRuntime.list() {
		known = known || a.Name == name
	}
	if !known {
		return "", false
	}
	path, err := hostAgentLogPath(name)
	if err != nil {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return strings.Join(lines, "\n"), true
}
