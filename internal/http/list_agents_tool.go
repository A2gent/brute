package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/tools"
)

type listAgentsTool struct{ server *Server }

func (t *listAgentsTool) Name() string { return "list_agents" }
func (t *listAgentsTool) Description() string {
	return "List configured host and Docker agents available to this session, including stopped agents that start automatically on delegation. No Docker or A2 Registry connection is required. Use the returned ID with delegate_to_agent. For remote agents use discover_external_agents."
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
	entries, err := t.server.configuredDelegationAgents(projectID)
	if err != nil {
		return nil, err
	}
	out := make([]configuredDelegationAgent, 0, len(entries))
	query := strings.ToLower(strings.TrimSpace(params.Query))
	for _, entry := range entries {
		if query == "" || strings.Contains(strings.ToLower(entry.ID+" "+entry.Name+" "+entry.Description), query) {
			out = append(out, entry)
		}
	}
	data, err := json.MarshalIndent(map[string]interface{}{"agents": out}, "", "  ")
	if err != nil {
		return nil, err
	}
	return &tools.Result{Success: true, Output: string(data)}, nil
}
