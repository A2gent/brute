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
	ProjectID  string `json:"project_id,omitempty"`
}

func newImportAgentDefinitionYAMLTool(server *Server) *importAgentDefinitionYAMLTool {
	return &importAgentDefinitionYAMLTool{server: server}
}

func (t *importAgentDefinitionYAMLTool) Name() string {
	return "import_agent_definition_yaml"
}

func (t *importAgentDefinitionYAMLTool) Description() string {
	return `Create or update a unified local agent definition from YAML. Use this for reusable Agents-page definitions, not one-off containers. Local definitions run in Docker; legacy runtime.type=host YAML is accepted only as a migration marker and is coerced to Docker. Provide either config_yaml or config_path. When run from a project session, the current project is used unless project_id is explicitly set. Prefer a reusable Soul folder such as agents/<agent-id>/agent.yaml for global definitions, or a project-local agents/<agent-id>/agent.yaml folder for project-specific definitions, optionally with adjacent skills/ and settings files. Passing the folder path is supported. Updating an existing definition removes stale managed Docker containers so the next run uses the new definition.`
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
				"description": "Path to a unified agent YAML file or definition folder. Global reusable source should live in Soul under agents/<agent-id>/agent.yaml; project-specific source should live under the bound project's agents/<agent-id>/agent.yaml, optionally with adjacent skills/.",
			},
			"project_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional project ID to bind the definition to. Defaults to the current session project, except system Soul/Body/Knowledge Base sessions remain global unless explicitly set.",
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

	projectID := strings.TrimSpace(p.ProjectID)
	if projectID == "" {
		projectID = t.inferSessionProjectID(ctx)
	}

	result, _, err := t.server.importAgentYAMLDefinition(ctx, importAgentYAMLRequest{
		ConfigYAML: p.ConfigYAML,
		ConfigPath: p.ConfigPath,
		ProjectID:  projectID,
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
			"project_id":         result.ProjectID,
			"removed_containers": result.RemovedContainers,
		},
	}, nil
}

func (t *importAgentDefinitionYAMLTool) inferSessionProjectID(ctx context.Context) string {
	if t == nil || t.server == nil || t.server.sessionManager == nil {
		return ""
	}
	sessionID, _ := ctx.Value("session_id").(string)
	if strings.TrimSpace(sessionID) == "" {
		return ""
	}
	sess, err := t.server.sessionManager.Get(sessionID)
	if err != nil || sess == nil || sess.ProjectID == nil {
		return ""
	}
	projectID := strings.TrimSpace(*sess.ProjectID)
	if isSystemAgentDefinitionComposerProject(projectID) {
		return ""
	}
	return projectID
}

func isSystemAgentDefinitionComposerProject(projectID string) bool {
	switch strings.TrimSpace(projectID) {
	case "system-soul", "system-agent", "system-kb":
		return true
	default:
		return false
	}
}

var _ tools.Tool = (*importAgentDefinitionYAMLTool)(nil)
