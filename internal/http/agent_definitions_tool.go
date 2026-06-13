package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/tools"
)

type importAgentDefinitionYAMLTool struct {
	server *Server
}

type importAgentDefinitionYAMLParams struct {
	ConfigYAML string `json:"config_yaml,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
}

func newImportAgentDefinitionYAMLTool(server *Server) *importAgentDefinitionYAMLTool {
	return &importAgentDefinitionYAMLTool{server: server}
}

func (t *importAgentDefinitionYAMLTool) Name() string {
	return "import_agent_definition_yaml"
}

func (t *importAgentDefinitionYAMLTool) Description() string {
	return `Create or update a unified local agent definition from YAML. Use this for reusable Agents-page definitions, not one-off containers. Local definitions run in Docker; legacy runtime.type=host YAML is accepted only as a migration marker and is coerced to Docker. Provide either config_yaml or config_path, preferably a reusable Soul file under agents/*.yaml. Updating an existing definition removes stale managed Docker containers so the next run uses the new definition.`
}

func (t *importAgentDefinitionYAMLTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"config_yaml": map[string]interface{}{
				"type":        "string",
				"description": "Inline unified agent definition YAML. Use this after drafting or editing the YAML content.",
			},
			"config_path": map[string]interface{}{
				"type":        "string",
				"description": "Path to a unified agent YAML file. Reusable source files should live in the Soul project under agents/*.yaml.",
			},
		},
	}
}

func (t *importAgentDefinitionYAMLTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p importAgentDefinitionYAMLParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if strings.TrimSpace(p.ConfigYAML) == "" && strings.TrimSpace(p.ConfigPath) == "" {
		return &tools.Result{Success: false, Error: "config_yaml or config_path is required"}, nil
	}

	result, _, err := t.server.importAgentYAMLDefinition(ctx, importAgentYAMLRequest{
		ConfigYAML: p.ConfigYAML,
		ConfigPath: p.ConfigPath,
	})
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode tool output: %w", err)
	}
	return &tools.Result{
		Success: true,
		Output:  string(encoded),
		Metadata: map[string]interface{}{
			"id":                 result.ID,
			"name":               result.Name,
			"created":            result.Created,
			"runtime":            result.Runtime,
			"removed_containers": result.RemovedContainers,
		},
	}, nil
}

var _ tools.Tool = (*importAgentDefinitionYAMLTool)(nil)
