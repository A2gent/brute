// agents_unified.go exposes the unified agent model: one list across saved
// agent configurations and local Docker containers, plus YAML export/import of
// canonical Docker agent definitions.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	agentDefinitionStatusUnhealthy  = "unhealthy"
	agentDefinitionStatusNotCreated = "not_created"
)

// UnifiedAgentResponse is one entry in the unified agents list.
type UnifiedAgentResponse struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	Runtime      string               `json:"runtime"`
	ProjectID    string               `json:"project_id,omitempty"`
	Status       string               `json:"status,omitempty"`
	AvatarURL    string               `json:"avatar_url,omitempty"`
	Running      *bool                `json:"running,omitempty"`
	APIURL       string               `json:"api_url,omitempty"`
	Managed      bool                 `json:"managed,omitempty"`
	HasContainer bool                 `json:"has_container,omitempty"`
	Definition   *agentdef.Definition `json:"definition,omitempty"`
	SubAgent     *SubAgentResponse    `json:"sub_agent,omitempty"`
	DockerAgent  *LocalDockerAgent    `json:"docker_agent,omitempty"`
	Containers   []LocalDockerAgent   `json:"containers,omitempty"`
}

type importAgentYAMLRequest struct {
	ConfigYAML string `json:"config_yaml"`
	ConfigPath string `json:"config_path"`
	ProjectID  string `json:"project_id,omitempty"`
}

type importAgentYAMLResult struct {
	Runtime           string               `json:"runtime"`
	Created           bool                 `json:"created"`
	ID                string               `json:"id"`
	Name              string               `json:"name"`
	ProjectID         string               `json:"project_id,omitempty"`
	Definition        *agentdef.Definition `json:"definition"`
	RemovedContainers []string             `json:"removed_containers"`
	Note              string               `json:"note"`
}

func unifiedAgentsProjectFilter(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		projectID = strings.TrimSpace(r.URL.Query().Get("projectID"))
	}
	return projectID, projectID != ""
}

func matchesUnifiedAgentProjectFilter(agentProjectID string, projectFilter string, filterHasProject bool) bool {
	agentProjectID = strings.TrimSpace(agentProjectID)
	projectFilter = strings.TrimSpace(projectFilter)
	if filterHasProject {
		return agentProjectID == projectFilter
	}
	return agentProjectID == ""
}

// agentVisibleInProjectSession reports whether a configured agent may appear in a
// session prompt or be delegated to from that session. Global agents (empty
// project) stay visible everywhere; project-bound agents are limited to their
// own project. This differs from matchesUnifiedAgentProjectFilter, which is for
// Agents UI lists and excludes globals when a project filter is set.
func agentVisibleInProjectSession(agentProjectID string, sessionProjectID string) bool {
	agentProjectID = strings.TrimSpace(agentProjectID)
	sessionProjectID = strings.TrimSpace(sessionProjectID)
	if agentProjectID == "" {
		return true
	}
	return agentProjectID == sessionProjectID
}

func storedAgentDefinitionMatchesProjectFilter(
	record *storage.AgentDefinitionRecord,
	def *agentdef.Definition,
	agentProjectID string,
	projectFilter string,
	filterHasProject bool,
	globalCatalogDir string,
	projectCatalogDir string,
) bool {
	if !matchesUnifiedAgentProjectFilter(agentProjectID, projectFilter, filterHasProject) {
		return false
	}
	if !filterHasProject {
		return true
	}
	return storedAgentDefinitionOwnedByProject(def, projectFilter, globalCatalogDir, projectCatalogDir)
}

func storedAgentDefinitionOwnedByProject(def *agentdef.Definition, projectFilter string, globalCatalogDir string, projectCatalogDir string) bool {
	if def == nil {
		return true
	}
	projectFilter = strings.TrimSpace(projectFilter)
	definitionDir := strings.TrimSpace(def.Local.DefinitionDir)

	// WHY: stored copies of global catalog agents can retain configured_project after
	// being started in a project, but they must stay in the global Agents view only.
	if definitionDir != "" && globalCatalogDir != "" && pathWithinDirectory(definitionDir, globalCatalogDir) {
		return false
	}
	if definitionDir != "" && projectCatalogDir != "" && pathWithinDirectory(definitionDir, projectCatalogDir) {
		return true
	}

	if strings.TrimSpace(def.Local.ProjectBindings[agentdef.WorkspaceScopeConfiguredProject]) == projectFilter {
		return true
	}
	switch strings.TrimSpace(def.Workspace.Scope) {
	case "", agentdef.WorkspaceScopeNone, agentdef.WorkspaceScopeCurrentProject, agentdef.WorkspaceScopeAllProjects, agentdef.WorkspaceScopeSelectedProjects:
		// WHY: global-runtime definitions must stay out of project listings unless they bind configured_project.
		return false
	default:
		return true
	}
}

func resolvedDiscoveredAgentProjectID(definitionProjectID string, projectFilter string, filterHasProject bool) string {
	definitionProjectID = strings.TrimSpace(definitionProjectID)
	projectFilter = strings.TrimSpace(projectFilter)
	if filterHasProject && definitionProjectID == "" {
		// WHY: definitions discovered under <project>/agents/ are project-local even when YAML omits configured_project.
		return projectFilter
	}
	return definitionProjectID
}

func subAgentProjectID(sa *storage.SubAgent) string {
	if sa == nil || sa.ProjectID == nil {
		return ""
	}
	return strings.TrimSpace(*sa.ProjectID)
}

func stringFromOptional(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func agentDefinitionRecordProjectID(record *storage.AgentDefinitionRecord) string {
	if record == nil || record.ProjectID == nil {
		return ""
	}
	return strings.TrimSpace(*record.ProjectID)
}

func applyUnifiedAgentContainerStatus(entry *UnifiedAgentResponse, containers []LocalDockerAgent) {
	entry.Containers = containers
	entry.HasContainer = len(containers) > 0
	entry.Status = agentDefinitionStatusStopped
	sawUnhealthyRunning := false
	for i := range containers {
		if !containers[i].Running {
			continue
		}
		if entry.APIURL == "" {
			entry.APIURL = containers[i].APIURL
		}
		if localDockerAgentAvailableForUse(containers[i]) {
			entry.Status = agentDefinitionStatusRunning
			entry.APIURL = containers[i].APIURL
			return
		}
		sawUnhealthyRunning = true
	}
	if sawUnhealthyRunning {
		entry.Status = agentDefinitionStatusUnhealthy
	}
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
	resolvedConfigPath := ""
	if strings.TrimSpace(req.ConfigPath) != "" {
		loaded, resolved, err := readLocalDockerAgentYAMLConfigFile(req.ConfigPath, "")
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		raw = loaded
		resolvedConfigPath = resolved
	}

	def, err := agentdef.ParseYAML(raw)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if strings.TrimSpace(def.Local.DefinitionDir) == "" {
		def.Local.DefinitionDir = inferAgentDefinitionSourceDir(req.ConfigPath, resolvedConfigPath, def)
	}
	if _, err := resolveAgentDefinitionSystemPrompt(def); err != nil {
		return nil, http.StatusBadRequest, err
	}
	projectID, err := s.normalizeAgentDefinitionProjectID(req.ProjectID)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if projectID != nil {
		bindDefinitionToProject(def, *projectID)
	}

	switch def.Runtime.Type {
	case agentdef.RuntimeHost, agentdef.RuntimeDocker:
		return s.importDockerAgentDefinition(ctx, def)
	default:
		return nil, http.StatusNotImplemented, fmt.Errorf("Importing remote runtime agents is not supported yet; register them through Matrix/A2A instead")
	}
}

func (s *Server) normalizeAgentDefinitionProjectID(raw string) (*string, error) {
	projectID := strings.TrimSpace(raw)
	if projectID == "" {
		return nil, nil
	}
	if _, err := s.store.GetProject(projectID); err != nil {
		return nil, fmt.Errorf("Project not found: %w", err)
	}
	return &projectID, nil
}

func bindDefinitionToProject(def *agentdef.Definition, projectID string) {
	if def == nil || strings.TrimSpace(projectID) == "" {
		return
	}
	projectID = strings.TrimSpace(projectID)
	def.Workspace.Scope = agentdef.WorkspaceScopeConfiguredProject
	if strings.TrimSpace(def.Workspace.Mount) == "" {
		def.Workspace.Mount = agentdef.WorkspaceMountRW
	}
	if def.Local.ProjectBindings == nil {
		def.Local.ProjectBindings = map[string]string{}
	}
	def.Local.ProjectBindings[agentdef.WorkspaceScopeConfiguredProject] = projectID
}

func projectIDFromDefinition(def *agentdef.Definition) *string {
	if def == nil || strings.TrimSpace(def.Workspace.Scope) != agentdef.WorkspaceScopeConfiguredProject {
		return nil
	}
	projectID := strings.TrimSpace(def.Local.ProjectBindings[agentdef.WorkspaceScopeConfiguredProject])
	if projectID == "" {
		return nil
	}
	return &projectID
}

func inferAgentDefinitionSourceDir(requestPath string, resolvedConfigPath string, def *agentdef.Definition) string {
	resolvedConfigPath = strings.TrimSpace(resolvedConfigPath)
	if resolvedConfigPath == "" {
		return ""
	}
	if rawRequest := strings.TrimSpace(requestPath); rawRequest != "" {
		candidate := expandHomePath(rawRequest)
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Clean(candidate)
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}

	dir := filepath.Dir(resolvedConfigPath)
	if def != nil {
		agentID := strings.TrimSpace(def.Agent.ID)
		if agentID != "" && filepath.Base(dir) == agentID {
			return absoluteCleanPath(dir, ".")
		}
	}
	for _, name := range localDockerAgentYAMLDirectoryConfigNames {
		if filepath.Base(resolvedConfigPath) == name {
			return absoluteCleanPath(dir, ".")
		}
	}
	return ""
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
		Runtime:        def.Runtime.Type,
		ProjectID:      projectIDFromDefinition(def),
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
	s.publishProjectResourcesChanged("agents", "")
	return &importAgentYAMLResult{
		Runtime:           def.Runtime.Type,
		Created:           created,
		ID:                record.ID,
		Name:              record.Name,
		ProjectID:         agentDefinitionRecordProjectID(record),
		Definition:        def,
		RemovedContainers: removedContainers,
		Note:              "The agent starts on first delegation and is reused warm per project binding.",
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

	projectID, _ := unifiedAgentsProjectFilter(r)
	def, discoveredProjectID, err := s.definitionForUnifiedAgent(id, projectID)
	if err != nil {
		status := http.StatusNotFound
		if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "failed to build") {
			status = http.StatusBadRequest
		}
		s.errorResponse(w, status, err.Error())
		return
	}
	if def.Runtime.Type != agentdef.RuntimeDocker && def.Runtime.Type != agentdef.RuntimeHost {
		s.errorResponse(w, http.StatusBadRequest, fmt.Sprintf("Agent %q uses runtime %q; only docker and host agents can be started locally", id, def.Runtime.Type))
		return
	}
	if strings.TrimSpace(def.Agent.ID) == "" {
		def.Agent.ID = id
	}

	startProjectID := strings.TrimSpace(projectID)
	if startProjectID == "" {
		startProjectID = strings.TrimSpace(discoveredProjectID)
	}
	if startProjectID == "" {
		if configuredProjectID := projectIDFromDefinition(def); configuredProjectID != nil {
			startProjectID = strings.TrimSpace(*configuredProjectID)
		}
	}

	agent, err := s.dockerRuntime.ensureAgentContainer(r.Context(), def, startProjectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to start agent: "+err.Error())
		return
	}
	s.publishProjectResourcesChanged("agents", startProjectID)
	s.jsonResponse(w, http.StatusOK, agent)
}

// handleDeleteAgentDefinition removes a stored or on-disk YAML definition and
// force-removes the warm containers the runtime manager created for it.
func (s *Server) handleDeleteAgentDefinition(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "agentDefID"))
	if id == "" {
		s.errorResponse(w, http.StatusBadRequest, "Agent ID is required")
		return
	}

	projectID, _ := unifiedAgentsProjectFilter(r)
	removedContainers := s.removeManagedContainersForAgentDefinition(r.Context(), id, "")

	if _, err := s.store.GetAgentDefinition(id); err == nil {
		if err := s.store.DeleteAgentDefinition(id); err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to delete agent definition: "+err.Error())
			return
		}
		s.publishProjectResourcesChanged("agents", "")
		s.jsonResponse(w, http.StatusOK, map[string]interface{}{
			"deleted":            true,
			"removed_containers": removedContainers,
		})
		return
	}

	location, err := s.findDiscoveredAgentDefinitionLocation(id, projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, err.Error())
		return
	}
	if err := deleteDiscoveredAgentDefinitionAtCatalog(location.Item, location.CatalogRoot); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete agent definition: "+err.Error())
		return
	}
	s.publishProjectResourcesChanged("agents", "")
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
	removedContainers := s.hostRuntime.stopForDefinition(defID, exceptName)
	containers, err := listLocalBruteContainers(ctx)
	if err != nil {
		logging.Warn("Failed to list containers for agent definition %s cleanup: %v", defID, err)
		return removedContainers
	}

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
		SystemPrompt: strings.TrimSpace(def.Instructions.System),
		HostPort:     def.Local.HostPort,
		AgentKind:    strings.TrimSpace(def.Agent.Kind),
		Labels: map[string]string{
			// WHY: local Docker containers are what Caesar registers in Square,
			// so portable definition metadata must ride along as Docker labels.
			"a2gent.agent_name":        strings.TrimSpace(def.Agent.Name),
			"a2gent.agent_emoji":       strings.TrimSpace(def.Agent.Emoji),
			"a2gent.agent_description": strings.TrimSpace(def.Agent.Description),
			"a2gent.agent_category":    strings.TrimSpace(def.Publish.Square.Category),
			"a2gent.agent_icon_url":    firstNonEmptyLocalAgentString(def.Publish.Square.IconURL, def.Agent.IconURL),
			"a2gent.agent_avatar_url":  firstNonEmptyLocalAgentString(def.Publish.Square.AvatarURL, def.Agent.AvatarURL),
		},
		LLM: localDockerAgentYAMLLLM{
			Provider:        def.LLM.Provider,
			Model:           def.LLM.Model,
			ReasoningEffort: def.LLM.ReasoningEffort,
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
		DefinitionDir: strings.TrimSpace(def.Local.DefinitionDir),
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
