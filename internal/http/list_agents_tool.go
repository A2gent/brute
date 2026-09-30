package http

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/tools"
)

type listAgentsTool struct{ server *Server }
type delegationAgentEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (t *listAgentsTool) Name() string { return "list_agents" }
func (t *listAgentsTool) Description() string {
	return "List configured local agents available to this session, including stopped agents that start on delegation. No Docker or A2 Registry connection is required. Use the returned ID with delegate_to_agent. For remote agents use discover_external_agents."
}
func (t *listAgentsTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{"query": map[string]interface{}{"type": "string", "description": "Optional case-insensitive name, ID or description filter."}}}
}
func (t *listAgentsTool) Execute(ctx context.Context, args json.RawMessage) (*tools.Result, error) {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	projectID := delegationParentProjectID(ctx, t.server)
	entries := map[string]delegationAgentEntry{}
	add := func(id, scope string, def *agentdef.Definition) {
		if def == nil || !agentVisibleInProjectSession(scope, projectID) {
			return
		}
		// Dispatch is the source of truth for ID precedence and project visibility.
		resolved, resolvedScope, err := t.server.definitionForUnifiedAgent(id, projectID)
		if err != nil || !agentVisibleInProjectSession(resolvedScope, projectID) {
			return
		}
		entries[id] = delegationAgentEntry{ID: id, Name: resolved.Agent.Name, Description: resolved.Agent.Description}
	}
	settings, err := t.server.store.GetSettings()
	if err != nil {
		return nil, err
	}
	dirs := []struct{ path, scope string }{{t.server.resolveGlobalAgentDefinitionsDirectory(settings), ""}}
	if projectID != "" {
		if project, err := t.server.store.GetProject(projectID); err == nil {
			dirs = append(dirs, struct{ path, scope string }{t.server.resolveScopedProjectAgentDefinitionsDirectory(project), projectID})
		}
	}
	for _, dir := range dirs {
		definitions, _ := discoverAgentDefinitionsInDirectory(dir.path)
		for _, item := range definitions {
			scope := item.ProjectID
			if scope == "" {
				scope = dir.scope
			}
			add(item.ID, scope, item.Definition)
		}
	}
	records, err := t.server.store.ListAgentDefinitions()
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record == nil {
			continue
		}
		def, err := agentdef.ParseYAML([]byte(record.DefinitionYAML))
		if err != nil {
			continue
		}
		scope := agentDefinitionRecordProjectID(record)
		if scope == "" {
			scope = stringFromOptional(projectIDFromDefinition(def))
		}
		add(record.ID, scope, def)
	}
	agents, err := t.server.store.ListSubAgents()
	if err != nil {
		return nil, err
	}
	for _, agent := range agents {
		if agent != nil {
			def, err := agentdef.FromSubAgent(agent)
			if err == nil {
				add(agent.ID, subAgentProjectID(agent), def)
			}
		}
	}
	out := make([]delegationAgentEntry, 0, len(entries))
	query := strings.ToLower(strings.TrimSpace(params.Query))
	for _, entry := range entries {
		if query == "" || strings.Contains(strings.ToLower(entry.ID+" "+entry.Name+" "+entry.Description), query) {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	data, err := json.MarshalIndent(map[string]interface{}{"agents": out}, "", "  ")
	if err != nil {
		return nil, err
	}
	return &tools.Result{Success: true, Output: string(data)}, nil
}
