package http

import (
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
)

func (s *Server) handleListUnifiedAgents(w http.ResponseWriter, r *http.Request) {
	agents := []UnifiedAgentResponse{}
	warnings := []string{}
	projectFilter, filterHasProject := unifiedAgentsProjectFilter(r)
	seenAgentIDs := make(map[string]struct{})

	dockerAgents, err := listLocalBruteContainers(r.Context())
	if err != nil {
		// Docker being unavailable should not hide saved configurations.
		warnings = append(warnings, "docker agents unavailable: "+err.Error())
	}
	// Host agents are listed like running containers so status logic is shared.
	dockerAgents = append(dockerAgents, s.hostRuntime.list()...)
	annotateLocalDockerAgentHealth(r.Context(), dockerAgents)

	containersByDefID := make(map[string][]LocalDockerAgent)
	for i := range dockerAgents {
		if defID := strings.TrimSpace(dockerAgents[i].Labels[dockerRuntimeAgentDefLabelKey]); defID != "" {
			containersByDefID[defID] = append(containersByDefID[defID], dockerAgents[i])
		}
	}

	appSettings, settingsErr := s.store.GetSettings()
	if settingsErr != nil {
		warnings = append(warnings, "app settings unavailable: "+settingsErr.Error())
		appSettings = map[string]string{}
	}

	globalCatalogDir := s.resolveGlobalAgentDefinitionsDirectory(appSettings)
	var definitionsDirectory string
	var projectCatalogDir string
	if filterHasProject {
		project, projectErr := s.store.GetProject(projectFilter)
		if projectErr != nil {
			warnings = append(warnings, "project unavailable for agent definitions scan: "+projectErr.Error())
		} else {
			definitionsDirectory = s.resolveScopedProjectAgentDefinitionsDirectory(project)
			projectCatalogDir = definitionsDirectory
		}
	} else {
		definitionsDirectory = globalCatalogDir
	}

	discoveredDefinitions, discoverWarnings := discoverAgentDefinitionsInDirectory(definitionsDirectory)
	warnings = append(warnings, discoverWarnings...)
	discoveredIDs := make(map[string]struct{}, len(discoveredDefinitions))
	for _, item := range discoveredDefinitions {
		discoveredIDs[item.ID] = struct{}{}
	}

	// WHY: the winner per ID follows definitionForUnifiedAgent (stored > legacy >
	// on-disk) and is chosen BEFORE project filtering, so a hidden winner cannot
	// let a lower-precedence entry with the same ID leak into the listing.
	// Entries are still emitted disk, legacy, stored to keep the response order.
	var diskAgents, legacyAgents, storedAgents []UnifiedAgentResponse

	definitions, err := s.store.ListAgentDefinitions()
	if err != nil {
		warnings = append(warnings, "stored agent definitions unavailable: "+err.Error())
	}
	for _, record := range definitions {
		if record == nil {
			continue
		}
		if _, exists := seenAgentIDs[record.ID]; exists {
			continue
		}
		seenAgentIDs[record.ID] = struct{}{}
		agentProjectID := agentDefinitionRecordProjectID(record)
		var parsedDef *agentdef.Definition
		if def, defErr := agentdef.ParseYAML([]byte(record.DefinitionYAML)); defErr == nil {
			parsedDef = def
			if agentProjectID == "" {
				agentProjectID = stringFromOptional(projectIDFromDefinition(def))
			}
		} else {
			warnings = append(warnings, "agent definition "+record.ID+": "+defErr.Error())
		}
		if _, onDisk := discoveredIDs[record.ID]; onDisk && !filterHasProject && parsedDef != nil {
			// A stored copy of a global catalog agent (started in a project) is the
			// same global agent as the on-disk entry, not a project-bound one.
			if d := strings.TrimSpace(parsedDef.Local.DefinitionDir); d != "" && globalCatalogDir != "" && pathWithinDirectory(d, globalCatalogDir) {
				agentProjectID = ""
			}
		}
		if !storedAgentDefinitionMatchesProjectFilter(record, parsedDef, agentProjectID, projectFilter, filterHasProject, globalCatalogDir, projectCatalogDir) {
			continue
		}
		entry := UnifiedAgentResponse{ID: record.ID, Name: record.Name, Runtime: record.Runtime, ProjectID: agentProjectID, Managed: true, Status: agentDefinitionStatusNotCreated}
		if parsedDef != nil {
			entry.Definition = parsedDef
		}
		if containers := containersByDefID[record.ID]; len(containers) > 0 {
			applyUnifiedAgentContainerStatus(&entry, containers)
		}
		storedAgents = append(storedAgents, entry)
	}

	subAgents, err := s.store.ListSubAgents()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list sub-agents: "+err.Error())
		return
	}
	for _, sa := range subAgents {
		if _, exists := seenAgentIDs[sa.ID]; exists {
			continue
		}
		seenAgentIDs[sa.ID] = struct{}{}
		agentProjectID := subAgentProjectID(sa)
		if !matchesUnifiedAgentProjectFilter(agentProjectID, projectFilter, filterHasProject) {
			continue
		}
		entry := UnifiedAgentResponse{ID: sa.ID, Name: sa.Name, Runtime: agentdef.RuntimeDocker, ProjectID: agentProjectID, Managed: true, Status: agentDefinitionStatusNotCreated}
		resp := s.subAgentToResponse(sa)
		entry.SubAgent = &resp
		if def, defErr := agentdef.FromSubAgent(sa); defErr == nil {
			entry.Definition = def
			if containers := containersByDefID[def.Agent.ID]; len(containers) > 0 {
				applyUnifiedAgentContainerStatus(&entry, containers)
			}
		} else {
			warnings = append(warnings, "sub-agent "+sa.ID+": "+defErr.Error())
		}
		legacyAgents = append(legacyAgents, entry)
	}

	for _, item := range discoveredDefinitions {
		if _, exists := seenAgentIDs[item.ID]; exists {
			continue
		}
		seenAgentIDs[item.ID] = struct{}{}
		agentProjectID := resolvedDiscoveredAgentProjectID(item.ProjectID, projectFilter, filterHasProject)
		if !matchesUnifiedAgentProjectFilter(agentProjectID, projectFilter, filterHasProject) {
			continue
		}
		entry := unifiedAgentResponseFromDiscoveredDefinition(item)
		entry.ProjectID = agentProjectID
		if containers := containersByDefID[item.ID]; len(containers) > 0 {
			applyUnifiedAgentContainerStatus(&entry, containers)
		}
		diskAgents = append(diskAgents, entry)
	}
	agents = append(agents, diskAgents...)
	agents = append(agents, legacyAgents...)
	agents = append(agents, storedAgents...)

	if !filterHasProject {
		for i := range dockerAgents {
			da := dockerAgents[i]
			if strings.TrimSpace(da.Labels[dockerRuntimeAgentDefLabelKey]) != "" {
				continue
			}
			running := da.Running
			status := da.Status
			if da.Running && !localDockerAgentAvailableForUse(da) {
				status = agentDefinitionStatusUnhealthy
			}
			agents = append(agents, UnifiedAgentResponse{
				ID: da.ID, Name: da.Name, Runtime: agentdef.RuntimeDocker, Status: status, Running: &running,
				APIURL: da.APIURL, DockerAgent: &dockerAgents[i], HasContainer: true,
			})
		}
	}

	for i := range agents {
		agents[i].AvatarURL = agentAvatarAPIPath(agents[i].Definition, agents[i].ID, projectFilter)
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"agents": agents, "warnings": warnings})
}
