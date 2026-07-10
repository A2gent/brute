// docker_runtime_manager.go keeps stored docker-runtime agent definitions
// running warm: containers are created once per agent/project binding, reused
// across delegations, and health-checked (docs/unified-agent-runtime-plan.md,
// Phase 3).
package http

import (
	"context"
	"fmt"
	nethttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

const (
	dockerRuntimeManagedLabelKey     = "a2gent.runtime_managed"
	dockerRuntimeAgentDefLabelKey    = "a2gent.agent_def_id"
	dockerRuntimeLLMProviderLabelKey = "a2gent.llm_provider"
	dockerRuntimeLLMModelLabelKey    = "a2gent.llm_model"
	dockerAgentIdleTimeoutEnvVar     = "A2GENT_DOCKER_AGENT_IDLE_TIMEOUT"
	dockerRuntimeReaperInterval      = 1 * time.Minute
	dockerRuntimeCreateTimeout       = 60 * time.Second
	dockerRuntimeHealthTimeout       = 30 * time.Second
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
		return 0
	}
	switch strings.ToLower(raw) {
	case "0", "off", "false", "disabled", "disable", "none", "never":
		return 0
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		logging.Warn("Invalid %s=%q, leaving Docker agent idle reaper disabled", dockerAgentIdleTimeoutEnvVar, raw)
		return 0
	}
	if parsed <= 0 {
		return 0
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

// resolveWorkspaceBinding applies the definition's single-project workspace policy
// at delegation time. Broad scopes are represented by a stable binding token for
// compatibility with older tests/helpers; Docker container creation uses
// Server.resolveDockerWorkspaceBinding so it can expand project folders.
func resolveWorkspaceBinding(def *agentdef.Definition, currentProjectID string) (projectID string, mount string, err error) {
	mount = normalizedDockerWorkspaceMount(def)
	switch strings.TrimSpace(def.Workspace.Scope) {
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
	case agentdef.WorkspaceScopeAllProjects:
		return dockerRuntimeAllProjectsBinding, mount, nil
	case agentdef.WorkspaceScopeSelectedProjects:
		if len(selectedProjectBindingIDs(def)) == 0 {
			return "", "", fmt.Errorf("agent %q uses selected_projects scope but local.project_bindings.selected_projects is not set", def.Agent.ID)
		}
		return dockerRuntimeSelectedProjectsBinding, mount, nil
	default:
		return "", "", fmt.Errorf("workspace scope %q is not supported by the docker runtime yet", def.Workspace.Scope)
	}
}

const (
	dockerRuntimeWorkspaceRoot           = "/workspace"
	dockerRuntimeAllProjectsBinding      = "all-projects"
	dockerRuntimeSelectedProjectsBinding = "selected-projects"
)

type dockerWorkspaceBinding struct {
	Scope                string
	ProjectID            string
	Mount                string
	ContainerNameBinding string
	ProjectMounts        []dockerWorkspaceProjectMount
}

type dockerWorkspaceProjectMount struct {
	ProjectID     string
	HostPath      string
	ContainerPath string
	Mode          string
}

func normalizedDockerWorkspaceMount(def *agentdef.Definition) string {
	mount := agentdef.WorkspaceMountRO
	if def != nil {
		if configured := strings.ToLower(strings.TrimSpace(def.Workspace.Mount)); configured != "" {
			mount = configured
		}
	}
	return mount
}

func selectedProjectBindingIDs(def *agentdef.Definition) []string {
	if def == nil || len(def.Local.ProjectBindings) == 0 {
		return nil
	}
	return splitProjectBindingList(def.Local.ProjectBindings[agentdef.WorkspaceScopeSelectedProjects])
}

func splitProjectBindingList(raw string) []string {
	seen := map[string]struct{}{}
	ids := []string{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func (s *Server) resolveDockerWorkspaceBinding(def *agentdef.Definition, currentProjectID string) (dockerWorkspaceBinding, error) {
	if def == nil {
		return dockerWorkspaceBinding{}, fmt.Errorf("agent definition is empty")
	}
	mount := normalizedDockerWorkspaceMount(def)
	scope := strings.TrimSpace(def.Workspace.Scope)
	binding := dockerWorkspaceBinding{Scope: scope, Mount: mount}

	switch scope {
	case "", agentdef.WorkspaceScopeNone:
		binding.Mount = ""
		return binding, nil
	case agentdef.WorkspaceScopeCurrentProject:
		projectID := strings.TrimSpace(currentProjectID)
		if projectID == "" {
			binding.Mount = ""
			return binding, nil
		}
		binding.ProjectID = projectID
		binding.ContainerNameBinding = projectID
		return binding, nil
	case agentdef.WorkspaceScopeConfiguredProject:
		projectID := strings.TrimSpace(def.Local.ProjectBindings[agentdef.WorkspaceScopeConfiguredProject])
		if projectID == "" {
			return binding, fmt.Errorf("agent %q uses configured_project scope but local.project_bindings.configured_project is not set", def.Agent.ID)
		}
		binding.ProjectID = projectID
		binding.ContainerNameBinding = projectID
		return binding, nil
	case agentdef.WorkspaceScopeSelectedProjects:
		ids := selectedProjectBindingIDs(def)
		if len(ids) == 0 {
			return binding, fmt.Errorf("agent %q uses selected_projects scope but local.project_bindings.selected_projects is not set", def.Agent.ID)
		}
		mounts, err := s.resolveSelectedProjectWorkspaceMounts(ids, mount)
		if err != nil {
			return binding, err
		}
		binding.ContainerNameBinding = dockerRuntimeSelectedProjectsBinding
		binding.ProjectMounts = mounts
		return binding, nil
	case agentdef.WorkspaceScopeAllProjects:
		mounts, err := s.resolveAllProjectWorkspaceMounts(mount)
		if err != nil {
			return binding, err
		}
		binding.ContainerNameBinding = dockerRuntimeAllProjectsBinding
		binding.ProjectMounts = mounts
		return binding, nil
	default:
		return binding, fmt.Errorf("workspace scope %q is not supported by the docker runtime yet", def.Workspace.Scope)
	}
}

func (s *Server) resolveAllProjectWorkspaceMounts(mount string) ([]dockerWorkspaceProjectMount, error) {
	projects, err := s.store.ListProjects()
	if err != nil {
		return nil, fmt.Errorf("failed to list projects for all_projects workspace: %w", err)
	}
	usedPaths := map[string]struct{}{}
	mounts := make([]dockerWorkspaceProjectMount, 0, len(projects))
	for _, project := range projects {
		if project == nil || project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
			continue
		}
		resolved, err := s.resolveProjectWorkspaceMount(project.ID, mount, usedPaths)
		if err != nil {
			logging.Warn("Docker runtime: skipping project %s for all_projects workspace: %v", project.ID, err)
			continue
		}
		mounts = append(mounts, resolved)
	}
	if len(mounts) == 0 {
		return nil, fmt.Errorf("workspace.scope all_projects found no configured project folders to mount")
	}
	return mounts, nil
}

func (s *Server) resolveSelectedProjectWorkspaceMounts(projectIDs []string, mount string) ([]dockerWorkspaceProjectMount, error) {
	usedPaths := map[string]struct{}{}
	mounts := make([]dockerWorkspaceProjectMount, 0, len(projectIDs))
	for _, projectID := range projectIDs {
		resolved, err := s.resolveProjectWorkspaceMount(projectID, mount, usedPaths)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, resolved)
	}
	return mounts, nil
}

func (s *Server) resolveProjectWorkspaceMount(projectID string, mount string, usedPaths map[string]struct{}) (dockerWorkspaceProjectMount, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return dockerWorkspaceProjectMount{}, fmt.Errorf("project id is empty")
	}
	hostPath, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		return dockerWorkspaceProjectMount{}, fmt.Errorf("project %s cannot be mounted: %w", projectID, err)
	}
	return dockerWorkspaceProjectMount{
		ProjectID:     projectID,
		HostPath:      hostPath,
		ContainerPath: dockerWorkspaceContainerPath(projectID, usedPaths),
		Mode:          mount,
	}, nil
}

func dockerWorkspaceContainerPath(projectID string, usedPaths map[string]struct{}) string {
	base := slugifyForDockerName(projectID)
	if base == "" {
		base = "project"
	}
	candidate := dockerRuntimeWorkspaceRoot + "/" + base
	if usedPaths == nil {
		return candidate
	}
	if _, ok := usedPaths[candidate]; !ok {
		usedPaths[candidate] = struct{}{}
		return candidate
	}
	for i := 2; ; i++ {
		candidate = fmt.Sprintf("%s/%s-%d", dockerRuntimeWorkspaceRoot, base, i)
		if _, ok := usedPaths[candidate]; !ok {
			usedPaths[candidate] = struct{}{}
			return candidate
		}
	}
}

// ensureAgentContainer returns a healthy warm container for the definition,
// creating or starting one only when needed — never `docker run` per delegation
// in the steady state.
func (m *dockerRuntimeManager) ensureAgentContainer(ctx context.Context, def *agentdef.Definition, currentProjectID string) (*LocalDockerAgent, error) {
	if def == nil || def.Runtime.Type != agentdef.RuntimeDocker {
		return nil, fmt.Errorf("definition is not a docker runtime agent")
	}

	workspace, err := m.server.resolveDockerWorkspaceBinding(def, currentProjectID)
	if err != nil {
		return nil, err
	}
	return m.ensureAgentContainerForWorkspace(ctx, def, workspace)
}

func (m *dockerRuntimeManager) ensureAgentContainerForWorkspace(ctx context.Context, def *agentdef.Definition, workspace dockerWorkspaceBinding) (*LocalDockerAgent, error) {
	if def == nil || def.Runtime.Type != agentdef.RuntimeDocker {
		return nil, fmt.Errorf("definition is not a docker runtime agent")
	}
	containerName := containerNameForAgent(def.Agent.ID, workspace.ContainerNameBinding)
	lock := m.creationLock(containerName)
	lock.Lock()
	defer lock.Unlock()

	if err := m.server.ensureSingleManagedContainerForAgentDefinition(ctx, def.Agent.ID, containerName); err != nil {
		return nil, err
	}

	agent, findErr := findLocalBruteContainer(ctx, containerName)
	var err error
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
			agent, err = m.createAgentContainer(ctx, def, containerName, workspace)
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
		agent, err = m.createAgentContainer(ctx, def, containerName, workspace)
		if err != nil {
			return nil, err
		}
	}

	if agent.HostPort == 0 || strings.TrimSpace(agent.APIURL) == "" {
		logging.Warn("Docker runtime: container %s has no published API port; recreating it", containerName)
		removeDockerContainerByName(ctx, containerName)
		agent, err = m.createAgentContainer(ctx, def, containerName, workspace)
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

func (m *dockerRuntimeManager) createAgentContainer(ctx context.Context, def *agentdef.Definition, containerName string, workspace dockerWorkspaceBinding) (*LocalDockerAgent, error) {
	req := localDockerCreateRequestBaseFromDefinition(def)
	req.Name = containerName
	req.ProjectID = workspace.ProjectID
	req.ProjectMountMode = ""
	prompt, promptErr := m.server.composeDockerAgentSystemPrompt(def, workspace.ProjectID)
	if promptErr != nil {
		return nil, promptErr
	}
	if strings.TrimSpace(prompt) != "" {
		req.SystemPrompt = strings.TrimSpace(prompt)
	}
	if workspace.ProjectID != "" {
		req.ProjectMountMode = workspace.Mount
	}
	if len(workspace.ProjectMounts) > 0 {
		// WHY: broad read-only agents need multiple project folders without giving
		// the container the whole host project parent. Mount each configured project
		// under /workspace/<project-id> using the existing explicit-volume path.
		for _, projectMount := range workspace.ProjectMounts {
			req.Directories.Volumes = append(req.Directories.Volumes, localDockerAgentYAMLVolumeMount{
				HostPath:      projectMount.HostPath,
				ContainerPath: projectMount.ContainerPath,
				Mode:          projectMount.Mode,
			})
		}
		req.SystemPrompt = appendDockerMultiProjectWorkspacePrompt(req.SystemPrompt, workspace.ProjectMounts)
	}
	if req.Labels == nil {
		req.Labels = map[string]string{}
	}
	provider, model := dockerDelegationLLMMetadataFromDefinition(def)
	if provider != "" {
		req.Labels[dockerRuntimeLLMProviderLabelKey] = provider
	}
	if model != "" {
		req.Labels[dockerRuntimeLLMModelLabelKey] = model
	}
	req.Labels[dockerRuntimeManagedLabelKey] = "true"
	req.Labels[dockerRuntimeAgentDefLabelKey] = def.Agent.ID
	if len(workspace.ProjectMounts) > 0 {
		req.Labels["a2gent.workspace_scope"] = workspace.Scope
	}

	createCtx, cancel := context.WithTimeout(ctx, dockerRuntimeCreateTimeout)
	logging.Info("Docker runtime: creating container %s for agent %s (workspace=%s project=%s mounts=%d mode=%s)", containerName, def.Agent.ID, workspace.Scope, workspace.ProjectID, len(workspace.ProjectMounts), workspace.Mount)
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

func (s *Server) composeDockerAgentSystemPrompt(def *agentdef.Definition, projectID string) (string, error) {
	if err := applyResolvedAgentDefinitionSystemPrompt(def); err != nil {
		return "", err
	}
	sa, err := agentdef.ToSubAgentConfig(def)
	if err != nil {
		logging.Warn("Docker runtime: failed to convert agent definition %s to rich prompt config: %v", def.Agent.ID, err)
		return strings.TrimSpace(def.Instructions.System), nil
	}
	promptSession := session.New("docker-agent")
	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		promptSession.ProjectID = &projectID
	}
	snapshot := s.composeSubAgentSystemPromptSnapshot(sa, promptSession)
	if snapshot == nil {
		return strings.TrimSpace(def.Instructions.System), nil
	}
	combined := strings.TrimSpace(snapshot.CombinedPrompt)
	combined = s.appendAgentDefinitionSkillsSection(combined, def)
	return strings.TrimSpace(s.rewriteDockerWorkspacePrompt(combined, projectID)), nil
}

func (s *Server) appendAgentDefinitionSkillsSection(prompt string, def *agentdef.Definition) string {
	if def == nil {
		return prompt
	}
	skillsDir := filepath.Join(strings.TrimSpace(def.Local.DefinitionDir), "skills")
	if !directoryExists(skillsDir) {
		return prompt
	}
	settings := map[string]string{skillsFolderSettingKey: skillsDir}
	section, _, resolveErr := s.resolveExternalMarkdownSkillsSection(settings, 0)
	if section == "" {
		if resolveErr != "" {
			logging.Warn("Docker runtime: failed to load definition skills for %s: %s", def.Agent.ID, resolveErr)
		}
		return prompt
	}
	if strings.Contains(prompt, skillsDir) {
		return prompt
	}
	return strings.TrimSpace(prompt) + "\n\n" + strings.TrimSpace(section)
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

func appendDockerMultiProjectWorkspacePrompt(prompt string, mounts []dockerWorkspaceProjectMount) string {
	prompt = strings.TrimSpace(prompt)
	if len(mounts) == 0 {
		return prompt
	}
	lines := []string{"Docker workspace note: multiple project roots are mounted read-only under /workspace:"}
	for _, mount := range mounts {
		if strings.TrimSpace(mount.ProjectID) == "" || strings.TrimSpace(mount.ContainerPath) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", mount.ProjectID, mount.ContainerPath))
	}
	if len(lines) == 1 {
		return prompt
	}
	note := strings.Join(lines, "\n")
	if strings.Contains(prompt, note) {
		return prompt
	}
	if prompt == "" {
		return note
	}
	return prompt + "\n\n" + note
}

// runIdleReaper periodically stops managed containers only when
// A2GENT_DOCKER_AGENT_IDLE_TIMEOUT is set to a positive duration. Containers
// the manager has never seen get a full timeout window from first sighting
// (covers server restarts).
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
	timeout := dockerAgentIdleTimeout()
	if timeout <= 0 {
		return
	}
	agents, err := listLocalBruteContainers(ctx)
	if err != nil {
		return
	}
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
