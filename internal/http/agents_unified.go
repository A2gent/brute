// agents_unified.go exposes the unified agent model: one list across saved
// agent configurations and local Docker containers, plus YAML export/import of
// canonical Docker agent definitions.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

// Lifecycle statuses for stored docker agent definitions.
const (
	agentDefinitionStatusRunning    = "running"
	agentDefinitionStatusStopped    = "stopped"
	agentDefinitionStatusNotCreated = "not_created"
)

// UnifiedAgentResponse is one entry in the unified agents list.
type UnifiedAgentResponse struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Runtime     string               `json:"runtime"`
	Status      string               `json:"status,omitempty"`
	Running     *bool                `json:"running,omitempty"`
	APIURL      string               `json:"api_url,omitempty"`
	Managed     bool                 `json:"managed,omitempty"`
	Definition  *agentdef.Definition `json:"definition,omitempty"`
	SubAgent    *SubAgentResponse    `json:"sub_agent,omitempty"`
	DockerAgent *LocalDockerAgent    `json:"docker_agent,omitempty"`
	Containers  []LocalDockerAgent   `json:"containers,omitempty"`
}

type importAgentYAMLRequest struct {
	ConfigYAML string `json:"config_yaml"`
	ConfigPath string `json:"config_path"`
}

type importAgentYAMLResult struct {
	Runtime           string               `json:"runtime"`
	Created           bool                 `json:"created"`
	ID                string               `json:"id"`
	Name              string               `json:"name"`
	Definition        *agentdef.Definition `json:"definition"`
	RemovedContainers []string             `json:"removed_containers"`
	Note              string               `json:"note"`
}

func (s *Server) handleListUnifiedAgents(w http.ResponseWriter, r *http.Request) {
	agents := []UnifiedAgentResponse{}
	warnings := []string{}

	dockerAgents, err := listLocalBruteContainers(r.Context())
	if err != nil {
		// Docker being unavailable should not hide saved configurations.
		warnings = append(warnings, "docker agents unavailable: "+err.Error())
	}

	containersByDefID := make(map[string][]LocalDockerAgent)
	for i := range dockerAgents {
		if defID := strings.TrimSpace(dockerAgents[i].Labels[dockerRuntimeAgentDefLabelKey]); defID != "" {
			containersByDefID[defID] = append(containersByDefID[defID], dockerAgents[i])
		}
	}

	subAgents, err := s.store.ListSubAgents()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list sub-agents: "+err.Error())
		return
	}
	for _, sa := range subAgents {
		entry := UnifiedAgentResponse{
			ID:      sa.ID,
			Name:    sa.Name,
			Runtime: agentdef.RuntimeDocker,
			Managed: true,
			Status:  agentDefinitionStatusNotCreated,
		}
		resp := s.subAgentToResponse(sa)
		entry.SubAgent = &resp
		if def, defErr := agentdef.FromSubAgent(sa); defErr == nil {
			entry.Definition = def
			if containers := containersByDefID[def.Agent.ID]; len(containers) > 0 {
				entry.Containers = containers
				entry.Status = agentDefinitionStatusStopped
				for i := range containers {
					if containers[i].Running {
						entry.Status = agentDefinitionStatusRunning
						entry.APIURL = containers[i].APIURL
						break
					}
				}
			}
		} else {
			warnings = append(warnings, "sub-agent "+sa.ID+": "+defErr.Error())
		}
		agents = append(agents, entry)
	}

	definitions, err := s.store.ListAgentDefinitions()
	if err != nil {
		warnings = append(warnings, "stored agent definitions unavailable: "+err.Error())
	}
	for _, record := range definitions {
		if record == nil {
			continue
		}
		entry := UnifiedAgentResponse{
			ID:      record.ID,
			Name:    record.Name,
			Runtime: record.Runtime,
			Managed: true,
			Status:  agentDefinitionStatusNotCreated,
		}
		if def, defErr := agentdef.ParseYAML([]byte(record.DefinitionYAML)); defErr == nil {
			entry.Definition = def
		} else {
			warnings = append(warnings, "agent definition "+record.ID+": "+defErr.Error())
		}
		if containers := containersByDefID[record.ID]; len(containers) > 0 {
			entry.Containers = containers
			entry.Status = agentDefinitionStatusStopped
			for i := range containers {
				if containers[i].Running {
					entry.Status = agentDefinitionStatusRunning
					entry.APIURL = containers[i].APIURL
					break
				}
			}
		}
		agents = append(agents, entry)
	}

	for i := range dockerAgents {
		da := dockerAgents[i]
		if strings.TrimSpace(da.Labels[dockerRuntimeAgentDefLabelKey]) != "" {
			continue // already attached to its definition entry
		}
		running := da.Running
		agents = append(agents, UnifiedAgentResponse{
			ID:          da.ID,
			Name:        da.Name,
			Runtime:     agentdef.RuntimeDocker,
			Status:      da.Status,
			Running:     &running,
			APIURL:      da.APIURL,
			DockerAgent: &dockerAgents[i],
		})
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"agents":   agents,
		"warnings": warnings,
	})
}

func (s *Server) handleExportSubAgentYAML(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "subAgentID")
	sa, err := s.store.GetSubAgent(id)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Sub-agent not found: "+err.Error())
		return
	}

	def, err := agentdef.FromSubAgent(sa)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to build agent definition: "+err.Error())
		return
	}
	raw, err := agentdef.ToYAML(def)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to encode agent definition: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"id":      sa.ID,
		"runtime": def.Runtime.Type,
		"yaml":    string(raw),
	})
}

func (s *Server) handleImportAgentYAML(w http.ResponseWriter, r *http.Request) {
	var req importAgentYAMLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	result, status, err := s.importAgentYAMLDefinition(r.Context(), req)
	if err != nil {
		s.errorResponse(w, status, err.Error())
		return
	}
	s.jsonResponse(w, status, result)
}

func (s *Server) importAgentYAMLDefinition(ctx context.Context, req importAgentYAMLRequest) (*importAgentYAMLResult, int, error) {
	raw := []byte(req.ConfigYAML)
	if strings.TrimSpace(req.ConfigPath) != "" {
		loaded, _, err := readLocalDockerAgentYAMLConfigFile(req.ConfigPath, "")
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		raw = loaded
	}

	def, err := agentdef.ParseYAML(raw)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	switch def.Runtime.Type {
	case agentdef.RuntimeHost:
		def.Runtime.Type = agentdef.RuntimeDocker
		if strings.TrimSpace(def.Runtime.Lifecycle) == "" {
			def.Runtime.Lifecycle = "warm"
		}
		if strings.TrimSpace(def.Workspace.Scope) == "" {
			def.Workspace.Scope = agentdef.WorkspaceScopeCurrentProject
			def.Workspace.Mount = agentdef.WorkspaceMountRW
		}
		return s.importDockerAgentDefinition(ctx, def)
	case agentdef.RuntimeDocker:
		return s.importDockerAgentDefinition(ctx, def)
	default:
		return nil, http.StatusNotImplemented, fmt.Errorf("Importing remote runtime agents is not supported yet; register them through Square/A2A instead")
	}
}

// importDockerAgentDefinition stores the definition as a local installation.
// The warm container is created lazily by the docker runtime manager on first
// delegation, so the workspace binding resolves against the real session.
func (s *Server) importDockerAgentDefinition(ctx context.Context, def *agentdef.Definition) (*importAgentYAMLResult, int, error) {
	if err := s.validateDockerDefinitionWorkspaceBindings(def); err != nil {
		return nil, http.StatusBadRequest, err
	}

	id := def.Agent.ID
	if id == "" {
		id = slugifyForDockerName(def.Agent.Name)
		def.Agent.ID = id
	}
	if id == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("agent.id or agent.name is required")
	}

	raw, err := agentdef.ToYAML(def)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	name := def.Agent.Name
	if name == "" {
		name = id
	}

	now := time.Now()
	created := true
	record := &storage.AgentDefinitionRecord{
		ID:             id,
		Name:           name,
		Runtime:        agentdef.RuntimeDocker,
		DefinitionYAML: string(raw),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if existing, getErr := s.store.GetAgentDefinition(id); getErr == nil && existing != nil {
		created = false
		record.CreatedAt = existing.CreatedAt
	}
	if err := s.store.SaveAgentDefinition(record); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("Failed to save agent definition: %w", err)
	}
	removedContainers := []string{}
	if !created {
		removedContainers = s.removeManagedContainersForAgentDefinition(ctx, id, "")
	}

	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	logging.Info("Imported docker agent definition: id=%s name=%s created=%v", record.ID, record.Name, created)
	return &importAgentYAMLResult{
		Runtime:           agentdef.RuntimeDocker,
		Created:           created,
		ID:                record.ID,
		Name:              record.Name,
		Definition:        def,
		RemovedContainers: removedContainers,
		Note:              "Container starts on first delegation and is reused warm per project binding.",
	}, status, nil
}

func (s *Server) validateDockerDefinitionWorkspaceBindings(def *agentdef.Definition) error {
	if def == nil {
		return fmt.Errorf("agent definition is empty")
	}
	switch strings.TrimSpace(def.Workspace.Scope) {
	case "", agentdef.WorkspaceScopeNone, agentdef.WorkspaceScopeCurrentProject:
		return nil
	case agentdef.WorkspaceScopeConfiguredProject:
		projectID := strings.TrimSpace(def.Local.ProjectBindings[agentdef.WorkspaceScopeConfiguredProject])
		if projectID == "" {
			return errAgentDefinitionMissingProjectBinding
		}
		if _, err := s.store.GetProject(projectID); err != nil {
			return fmt.Errorf("Agent definition binds unknown project %s; fix local.project_bindings or remove the binding", projectID)
		}
		return nil
	case agentdef.WorkspaceScopeSelectedProjects:
		projectIDs := selectedProjectBindingIDs(def)
		if len(projectIDs) == 0 {
			return &agentDefinitionImportError{"workspace.scope is selected_projects but local.project_bindings.selected_projects is not set"}
		}
		for _, projectID := range projectIDs {
			if _, err := s.store.GetProject(projectID); err != nil {
				return fmt.Errorf("Agent definition binds unknown project %s; fix local.project_bindings.selected_projects or remove the binding", projectID)
			}
		}
		return nil
	case agentdef.WorkspaceScopeAllProjects:
		return nil
	default:
		return errAgentDefinitionWorkspaceScopeUnsupported(def.Workspace.Scope)
	}
}

func (s *Server) handleExportAgentDefinitionYAML(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "agentDefID")
	record, err := s.store.GetAgentDefinition(id)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Agent definition not found: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"id":      record.ID,
		"runtime": record.Runtime,
		"yaml":    record.DefinitionYAML,
	})
}

func (s *Server) handleStartUnifiedAgent(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "agentDefID"))
	if id == "" {
		s.errorResponse(w, http.StatusBadRequest, "Agent ID is required")
		return
	}

	def, err := s.definitionForUnifiedAgent(id)
	if err != nil {
		status := http.StatusNotFound
		if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "failed to build") {
			status = http.StatusBadRequest
		}
		s.errorResponse(w, status, err.Error())
		return
	}
	if def.Runtime.Type != agentdef.RuntimeDocker {
		s.errorResponse(w, http.StatusBadRequest, fmt.Sprintf("Agent %q uses runtime %q; only docker agents can be started locally", id, def.Runtime.Type))
		return
	}
	if strings.TrimSpace(def.Agent.ID) == "" {
		def.Agent.ID = id
	}

	agent, err := s.dockerRuntime.ensureAgentContainer(r.Context(), def, "")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to start agent: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, agent)
}

func (s *Server) definitionForUnifiedAgent(id string) (*agentdef.Definition, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("agent ID is required")
	}

	if sa, err := s.store.GetSubAgent(id); err == nil && sa != nil {
		def, defErr := agentdef.FromSubAgent(sa)
		if defErr != nil {
			return nil, fmt.Errorf("failed to build agent definition from saved agent %s: %w", id, defErr)
		}
		return def, nil
	}

	record, err := s.store.GetAgentDefinition(id)
	if err != nil {
		return nil, fmt.Errorf("agent definition not found: %s", id)
	}
	def, err := agentdef.ParseYAML([]byte(record.DefinitionYAML))
	if err != nil {
		return nil, fmt.Errorf("stored agent definition %s is invalid: %w", id, err)
	}
	return def, nil
}

// handleDeleteAgentDefinition removes a stored definition and force-removes
// the warm containers the runtime manager created for it.
func (s *Server) handleDeleteAgentDefinition(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "agentDefID")
	if _, err := s.store.GetAgentDefinition(id); err != nil {
		s.errorResponse(w, http.StatusNotFound, "Agent definition not found: "+err.Error())
		return
	}

	removedContainers := s.removeManagedContainersForAgentDefinition(r.Context(), id, "")

	if err := s.store.DeleteAgentDefinition(id); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete agent definition: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"deleted":            true,
		"removed_containers": removedContainers,
	})
}

func (s *Server) removeManagedContainersForAgentDefinition(ctx context.Context, defID string, exceptName string) []string {
	defID = strings.TrimSpace(defID)
	exceptName = strings.TrimSpace(exceptName)
	if defID == "" {
		return nil
	}
	containers, err := listLocalBruteContainers(ctx)
	if err != nil {
		logging.Warn("Failed to list containers for agent definition %s cleanup: %v", defID, err)
		return nil
	}

	removedContainers := []string{}
	for _, container := range containers {
		if container.Labels[dockerRuntimeAgentDefLabelKey] != defID {
			continue
		}
		if exceptName != "" && container.Name == exceptName {
			continue
		}
		rmCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, rmErr := runCommand(rmCtx, "docker", "rm", "-f", container.ID)
		cancel()
		if rmErr == nil {
			removedContainers = append(removedContainers, container.Name)
		} else {
			logging.Warn("Failed to remove container %s for agent definition %s: %v", container.Name, defID, rmErr)
		}
	}
	return removedContainers
}

func (s *Server) ensureSingleManagedContainerForAgentDefinition(ctx context.Context, defID string, containerName string) error {
	removed := s.removeManagedContainersForAgentDefinition(ctx, defID, containerName)
	if len(removed) > 0 {
		logging.Info("Docker runtime: removed stale containers for agent definition %s: %s", defID, strings.Join(removed, ", "))
	}
	for _, name := range removed {
		if name == containerName {
			return fmt.Errorf("removed target container %s while enforcing single instance", containerName)
		}
	}
	return nil
}

// localDockerCreateRequestBaseFromDefinition maps everything except the
// workspace binding, which import and the docker runtime manager resolve
// differently (eagerly vs. at delegation time).
func localDockerCreateRequestBaseFromDefinition(def *agentdef.Definition) createLocalDockerAgentRequest {
	name := slugifyForDockerName(def.Agent.ID)
	if name == "" {
		name = slugifyForDockerName(def.Agent.Name)
	}

	req := createLocalDockerAgentRequest{
		Name:         name,
		Image:        strings.TrimSpace(def.Runtime.Image),
		HostPort:     def.Local.HostPort,
		AgentKind:    strings.TrimSpace(def.Agent.Kind),
		SystemPrompt: strings.TrimSpace(def.Instructions.System),
		LLM: localDockerAgentYAMLLLM{
			Provider: def.LLM.Provider,
			Model:    def.LLM.Model,
		},
		Tools: localDockerAgentYAMLTools{
			Mode:     def.Tools.Mode,
			Enabled:  append([]string(nil), def.Tools.Enabled...),
			Disabled: append([]string(nil), def.Tools.Disabled...),
		},
		Resources: localDockerAgentYAMLResources{
			CPUs:   def.Runtime.Resources.CPUs,
			Memory: def.Runtime.Resources.Memory,
			GPUs:   def.Runtime.Resources.GPUs,
		},
		Networking: localDockerAgentYAMLNetworking{
			InternetAccess: def.Networking.InternetAccess,
		},
	}

	if len(def.Local.Credentials) > 0 {
		req.Credentials = make(map[string]localDockerAgentCredential, len(def.Local.Credentials))
		for key, cred := range def.Local.Credentials {
			req.Credentials[key] = localDockerAgentCredential{Env: cred.Env, File: cred.File}
		}
	}

	return req
}

// localDockerCreateRequestFromDefinition maps a canonical docker-runtime
// definition onto the existing local Docker agent create request. Broad
// multi-project scopes resolve later in the runtime manager because import does
// not know which containers must be created yet.
func localDockerCreateRequestFromDefinition(def *agentdef.Definition) (createLocalDockerAgentRequest, error) {
	req := localDockerCreateRequestBaseFromDefinition(def)

	switch def.Workspace.Scope {
	case "", agentdef.WorkspaceScopeNone, agentdef.WorkspaceScopeCurrentProject:
		// current_project resolves at delegation time; nothing to mount on import.
	case agentdef.WorkspaceScopeConfiguredProject:
		projectID := strings.TrimSpace(def.Local.ProjectBindings[agentdef.WorkspaceScopeConfiguredProject])
		if projectID == "" {
			return req, errAgentDefinitionMissingProjectBinding
		}
		req.ProjectID = projectID
		req.ProjectMountMode = def.Workspace.Mount
	case agentdef.WorkspaceScopeSelectedProjects:
		if len(selectedProjectBindingIDs(def)) == 0 {
			return req, &agentDefinitionImportError{"workspace.scope is selected_projects but local.project_bindings.selected_projects is not set"}
		}
		// selected_projects expands to per-project volumes at delegation time.
	case agentdef.WorkspaceScopeAllProjects:
		// all_projects expands to per-project volumes at delegation time.
	default:
		return req, errAgentDefinitionWorkspaceScopeUnsupported(def.Workspace.Scope)
	}

	return req, nil
}
var errAgentDefinitionMissingProjectBinding = &agentDefinitionImportError{
	"workspace.scope is configured_project but local.project_bindings.configured_project is not set",
}

func errAgentDefinitionWorkspaceScopeUnsupported(scope string) error {
	return &agentDefinitionImportError{"workspace.scope " + scope + " is not supported for docker import yet"}
}

type agentDefinitionImportError struct{ msg string }

func (e *agentDefinitionImportError) Error() string { return e.msg }
