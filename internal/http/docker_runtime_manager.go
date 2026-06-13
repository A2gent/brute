// docker_runtime_manager.go keeps stored docker-runtime agent definitions
// running warm: containers are created once per agent/project binding, reused
// across delegations, health-checked, and stopped again after an idle timeout
// (docs/unified-agent-runtime-plan.md, Phase 3).
package http

import (
	"context"
	"fmt"
	nethttp "net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

const (
	dockerRuntimeManagedLabelKey  = "a2gent.runtime_managed"
	dockerRuntimeAgentDefLabelKey = "a2gent.agent_def_id"
	defaultDockerAgentIdleTimeout = 30 * time.Minute
	dockerAgentIdleTimeoutEnvVar  = "A2GENT_DOCKER_AGENT_IDLE_TIMEOUT"
	dockerRuntimeReaperInterval   = 1 * time.Minute
	dockerRuntimeCreateTimeout    = 60 * time.Second
	dockerRuntimeHealthTimeout    = 30 * time.Second
)

type dockerRuntimeManager struct {
	server *Server

	mu       sync.Mutex
	lastUsed map[string]time.Time // container name -> last delegation or first sighting
	creating map[string]*sync.Mutex
}

func newDockerRuntimeManager(server *Server) *dockerRuntimeManager {
	return &dockerRuntimeManager{
		server:   server,
		lastUsed: make(map[string]time.Time),
		creating: make(map[string]*sync.Mutex),
	}
}

// containerNameForAgent includes the project binding so warm reuse never picks
// a container mounted to the wrong project (e.g. agent-code-reviewer__project-brute).
func containerNameForAgent(defID string, projectID string) string {
	name := "agent-" + slugifyForDockerName(defID)
	if strings.TrimSpace(projectID) != "" {
		name += "__project-" + slugifyForDockerName(projectID)
	}
	return name
}

func dockerAgentIdleTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(dockerAgentIdleTimeoutEnvVar))
	if raw == "" {
		return defaultDockerAgentIdleTimeout
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		logging.Warn("Invalid %s=%q, using default %s", dockerAgentIdleTimeoutEnvVar, raw, defaultDockerAgentIdleTimeout)
		return defaultDockerAgentIdleTimeout
	}
	return parsed
}

func (m *dockerRuntimeManager) touch(containerName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUsed[containerName] = time.Now()
}

func (m *dockerRuntimeManager) lastUsedAt(containerName string) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.lastUsed[containerName]
	return at, ok
}

// creationLock serializes ensure calls per container name so parallel
// delegations to the same agent never race two `docker run`s.
func (m *dockerRuntimeManager) creationLock(containerName string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.creating[containerName]
	if !ok {
		lock = &sync.Mutex{}
		m.creating[containerName] = lock
	}
	return lock
}

// resolveWorkspaceBinding applies the definition's workspace policy at
// delegation time. currentProjectID is the parent session's project, used only
// for the current_project scope.
func resolveWorkspaceBinding(def *agentdef.Definition, currentProjectID string) (projectID string, mount string, err error) {
	mount = def.Workspace.Mount
	if mount == "" {
		mount = agentdef.WorkspaceMountRO
	}
	switch def.Workspace.Scope {
	case "", agentdef.WorkspaceScopeNone:
		return "", "", nil
	case agentdef.WorkspaceScopeCurrentProject:
		projectID = strings.TrimSpace(currentProjectID)
		if projectID == "" {
			// No parent project means there is nothing to mount; run without a
			// workspace rather than failing the delegation.
			return "", "", nil
		}
		return projectID, mount, nil
	case agentdef.WorkspaceScopeConfiguredProject:
		projectID = strings.TrimSpace(def.Local.ProjectBindings[agentdef.WorkspaceScopeConfiguredProject])
		if projectID == "" {
			return "", "", fmt.Errorf("agent %q uses configured_project scope but local.project_bindings.configured_project is not set", def.Agent.ID)
		}
		return projectID, mount, nil
	default:
		return "", "", fmt.Errorf("workspace scope %q is not supported by the docker runtime yet", def.Workspace.Scope)
	}
}

// ensureAgentContainer returns a healthy warm container for the definition,
// creating or starting one only when needed — never `docker run` per delegation
// in the steady state.
func (m *dockerRuntimeManager) ensureAgentContainer(ctx context.Context, def *agentdef.Definition, currentProjectID string) (*LocalDockerAgent, error) {
	if def == nil || def.Runtime.Type != agentdef.RuntimeDocker {
		return nil, fmt.Errorf("definition is not a docker runtime agent")
	}

	projectID, mount, err := resolveWorkspaceBinding(def, currentProjectID)
	if err != nil {
		return nil, err
	}

	containerName := containerNameForAgent(def.Agent.ID, projectID)
	lock := m.creationLock(containerName)
	lock.Lock()
	defer lock.Unlock()

	if err := m.server.ensureSingleManagedContainerForAgentDefinition(ctx, def.Agent.ID, containerName); err != nil {
		return nil, err
	}

	agent, findErr := findLocalBruteContainer(ctx, containerName)
	switch {
	case findErr == nil && agent.Running:
		// Warm reuse.
	case findErr == nil && !agent.Running:
		startCtx, cancel := context.WithTimeout(ctx, dockerDelegationStartTimeout)
		_, startErr := runCommand(startCtx, "docker", "start", agent.ID)
		cancel()
		if startErr != nil {
			if !isDockerPortAllocationError(startErr) {
				return nil, fmt.Errorf("failed to start container %s: %w", containerName, startErr)
			}
			logging.Warn("Docker runtime: stopped container %s cannot restart because its host port is unavailable; recreating with automatic port selection", containerName)
			removeDockerContainerByName(ctx, containerName)
			agent, err = m.createAgentContainer(ctx, def, containerName, projectID, mount)
			if err != nil {
				return nil, err
			}
			break
		}
		agent, findErr = findLocalBruteContainer(ctx, containerName)
		if findErr != nil {
			return nil, fmt.Errorf("container %s started but could not be inspected: %w", containerName, findErr)
		}
	default:
		agent, err = m.createAgentContainer(ctx, def, containerName, projectID, mount)
		if err != nil {
			return nil, err
		}
	}

	if agent.HostPort == 0 || strings.TrimSpace(agent.APIURL) == "" {
		logging.Warn("Docker runtime: container %s has no published API port; recreating it", containerName)
		removeDockerContainerByName(ctx, containerName)
		agent, err = m.createAgentContainer(ctx, def, containerName, projectID, mount)
		if err != nil {
			return nil, err
		}
	}
	if agent.HostPort == 0 || strings.TrimSpace(agent.APIURL) == "" {
		return nil, fmt.Errorf("container %s does not expose port 8080 on the host", containerName)
	}

	healthCtx, cancel := context.WithTimeout(ctx, dockerRuntimeHealthTimeout)
	defer cancel()
	client := &nethttp.Client{Timeout: dockerRuntimeHealthTimeout}
	if err := waitForLocalDockerAgentHTTP(healthCtx, client, agent.APIURL); err != nil {
		return nil, fmt.Errorf("container %s is unhealthy: %w", containerName, err)
	}

	m.touch(containerName)
	return agent, nil
}

func (m *dockerRuntimeManager) createAgentContainer(ctx context.Context, def *agentdef.Definition, containerName string, projectID string, mount string) (*LocalDockerAgent, error) {
	req := localDockerCreateRequestBaseFromDefinition(def)
	req.Name = containerName
	req.ProjectID = projectID
	req.ProjectMountMode = ""
	if prompt := strings.TrimSpace(m.server.composeDockerAgentSystemPrompt(def, projectID)); prompt != "" {
		req.SystemPrompt = prompt
	}
	if projectID != "" {
		req.ProjectMountMode = mount
	}
	if req.Labels == nil {
		req.Labels = map[string]string{}
	}
	req.Labels[dockerRuntimeManagedLabelKey] = "true"
	req.Labels[dockerRuntimeAgentDefLabelKey] = def.Agent.ID

	createCtx, cancel := context.WithTimeout(ctx, dockerRuntimeCreateTimeout)
	logging.Info("Docker runtime: creating container %s for agent %s (project=%s mount=%s)", containerName, def.Agent.ID, projectID, mount)
	result, _, createErr := m.server.createLocalDockerAgent(createCtx, req)
	cancel()
	if createErr != nil && req.HostPort > 0 && isDockerPortAllocationError(createErr) {
		configuredHostPort := req.HostPort
		logging.Warn("Docker runtime: host port %d is unavailable for agent %s; retrying with automatic port selection", configuredHostPort, def.Agent.ID)
		removeDockerContainerByName(ctx, containerName)

		retryReq := req
		retryReq.HostPort = 0
		retryCtx, retryCancel := context.WithTimeout(ctx, dockerRuntimeCreateTimeout)
		result, _, createErr = m.server.createLocalDockerAgent(retryCtx, retryReq)
		retryCancel()
	}
	if createErr != nil {
		return nil, fmt.Errorf("failed to create container %s: %w", containerName, createErr)
	}
	if result.Agent != nil {
		return result.Agent, nil
	}
	agent, findErr := findLocalBruteContainer(ctx, containerName)
	if findErr != nil {
		return nil, fmt.Errorf("container %s created but could not be inspected: %w", containerName, findErr)
	}
	return agent, nil
}

func isDockerPortAllocationError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "port is already allocated") ||
		strings.Contains(message, "address already in use") ||
		strings.Contains(message, "bind for 0.0.0.0")
}

func removeDockerContainerByName(ctx context.Context, name string) {
	name = strings.TrimSpace(name)
	if name == "" || !dockerContainerIDPattern.MatchString(name) {
		return
	}
	rmCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := runCommand(rmCtx, "docker", "rm", "-f", name); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "no such container") {
			logging.Warn("Docker runtime: failed to clean up container %s before retry: %v", name, err)
		}
	}
}

func (s *Server) composeDockerAgentSystemPrompt(def *agentdef.Definition, projectID string) string {
	sa, err := agentdef.ToSubAgentConfig(def)
	if err != nil {
		logging.Warn("Docker runtime: failed to convert agent definition %s to rich prompt config: %v", def.Agent.ID, err)
		return strings.TrimSpace(def.Instructions.System)
	}
	promptSession := session.New("docker-agent")
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		promptSession.ProjectID = &projectID
	}
	snapshot := s.composeSubAgentSystemPromptSnapshot(sa, promptSession)
	if snapshot == nil {
		return strings.TrimSpace(def.Instructions.System)
	}
	return strings.TrimSpace(s.rewriteDockerWorkspacePrompt(snapshot.CombinedPrompt, projectID))
}

func (s *Server) rewriteDockerWorkspacePrompt(prompt string, projectID string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || strings.TrimSpace(projectID) == "" {
		return prompt
	}

	if project, err := s.store.GetProject(strings.TrimSpace(projectID)); err == nil && project != nil && project.Folder != nil {
		hostRoot := absoluteCleanPath(strings.TrimSpace(*project.Folder), strings.TrimSpace(s.config.WorkDir))
		if hostRoot != "" {
			prompt = strings.ReplaceAll(prompt, hostRoot, "/workspace")
		}
	}

	hostWorkDir := absoluteCleanPath(strings.TrimSpace(s.config.WorkDir), ".")
	if hostWorkDir != "" {
		prompt = strings.ReplaceAll(prompt, "- Server working directory: "+hostWorkDir, "- Server working directory: /workspace")
	}
	const dockerWorkspaceNote = "Docker workspace note: operate inside /workspace; this is the mounted project root for this delegated agent."
	if !strings.Contains(prompt, dockerWorkspaceNote) {
		prompt += "\n\n" + dockerWorkspaceNote
	}
	return prompt
}

// runIdleReaper periodically stops managed containers that have not received a
// delegation within the idle timeout. Containers the manager has never seen get
// a full timeout window from first sighting (covers server restarts).
func (m *dockerRuntimeManager) runIdleReaper(ctx context.Context) {
	ticker := time.NewTicker(dockerRuntimeReaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reapIdleContainers(ctx)
		}
	}
}

func (m *dockerRuntimeManager) reapIdleContainers(ctx context.Context) {
	agents, err := listLocalBruteContainers(ctx)
	if err != nil {
		return
	}
	timeout := dockerAgentIdleTimeout()
	now := time.Now()
	for i := range agents {
		agent := agents[i]
		if !agent.Running || !strings.EqualFold(agent.Labels[dockerRuntimeManagedLabelKey], "true") {
			continue
		}
		last, known := m.lastUsedAt(agent.Name)
		if !known {
			m.touch(agent.Name)
			continue
		}
		if now.Sub(last) < timeout {
			continue
		}
		stopCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, stopErr := runCommand(stopCtx, "docker", "stop", agent.ID)
		cancel()
		if stopErr != nil {
			logging.Warn("Docker runtime: failed to stop idle container %s: %v", agent.Name, stopErr)
			continue
		}
		logging.Info("Docker runtime: stopped idle container %s (idle for %s)", agent.Name, now.Sub(last).Round(time.Second))
	}
}
