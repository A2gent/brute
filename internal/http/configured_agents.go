package http

import (
	"sort"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
)

type configuredDelegationAgent struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Metrics     agentdef.AgentMetrics `json:"-"`
}

// configuredDelegationAgents shares the dispatcher's definition precedence and
// project visibility. Availability is based on configuration, not live process
// health: both host processes and Docker containers start lazily on delegation.
func (s *Server) configuredDelegationAgents(projectID string) ([]configuredDelegationAgent, error) {
	entries := map[string]configuredDelegationAgent{}
	add := func(id string) {
		if _, exists := entries[id]; exists {
			return
		}
		def, scope, err := s.definitionForUnifiedAgent(id, projectID)
		if err != nil || !agentVisibleInProjectSession(scope, projectID) {
			return
		}
		if def.Runtime.Type != agentdef.RuntimeHost && def.Runtime.Type != agentdef.RuntimeDocker {
			return
		}
		name := strings.TrimSpace(def.Agent.Name)
		if name == "" {
			name = id
		}
		entries[id] = configuredDelegationAgent{ID: id, Name: name, Description: def.Agent.Description, Metrics: def.Metrics}
	}
	settings, err := s.store.GetSettings()
	if err != nil {
		return nil, err
	}
	dirs := []string{s.resolveGlobalAgentDefinitionsDirectory(settings)}
	if projectID != "" {
		if project, err := s.store.GetProject(projectID); err == nil {
			dirs = append(dirs, s.resolveScopedProjectAgentDefinitionsDirectory(project))
		}
	}
	for _, dir := range dirs {
		definitions, _ := discoverAgentDefinitionsInDirectory(dir)
		for _, item := range definitions {
			add(item.ID)
		}
	}

	records, err := s.store.ListAgentDefinitions()
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record != nil {
			add(record.ID)
		}
	}
	agents, err := s.store.ListSubAgents()
	if err != nil {
		return nil, err
	}
	for _, agent := range agents {
		if agent != nil {
			add(agent.ID)
		}
	}
	out := make([]configuredDelegationAgent, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
