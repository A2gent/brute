package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/tools"
)

type createLocalDockerAgentsBulkTool struct {
	server *Server
}

type createLocalDockerAgentsBulkParams struct {
	Agents           []createLocalDockerAgentsBulkSpec `json:"agents"`
	NamePrefix       string                            `json:"name_prefix,omitempty"`
	StartPort        int                               `json:"start_port,omitempty"`
	Image            string                            `json:"image,omitempty"`
	LMStudioBaseURL  string                            `json:"lm_studio_base_url,omitempty"`
	SessionID        string                            `json:"session_id,omitempty"`
	ProjectID        string                            `json:"project_id,omitempty"`
	ProjectMountMode string                            `json:"project_mount_mode,omitempty"`
	ContinueOnError  *bool                             `json:"continue_on_error,omitempty"`
}

type createLocalDockerAgentsBulkSpec struct {
	Name         string `json:"name,omitempty"`
	AgentKind    string `json:"agent_kind,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	HostPort     int    `json:"host_port,omitempty"`
}

func newCreateLocalDockerAgentsBulkTool(server *Server) *createLocalDockerAgentsBulkTool {
	return &createLocalDockerAgentsBulkTool{server: server}
}

func (t *createLocalDockerAgentsBulkTool) Name() string {
	return "create_local_docker_agents_bulk"
}

func (t *createLocalDockerAgentsBulkTool) Description() string {
	return `Create multiple local Dockerized Brute agents in one call. Useful for role-based batches (researcher/planner/reviewer/etc). You can provide explicit names and ports per agent, or use name_prefix/start_port for auto-generated names and sequential ports.`
}

func (t *createLocalDockerAgentsBulkTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agents": map[string]interface{}{
				"type":        "array",
				"description": "List of local agents to create.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]interface{}{
							"type":        "string",
							"description": "Optional container name. If omitted, a name is generated.",
						},
						"agent_kind": map[string]interface{}{
							"type":        "string",
							"description": "Optional role label (researcher/planner/reviewer/etc).",
						},
						"system_prompt": map[string]interface{}{
							"type":        "string",
							"description": "Optional initial system prompt for this agent.",
						},
						"host_port": map[string]interface{}{
							"type":        "integer",
							"description": "Optional explicit host port for this agent.",
						},
					},
				},
			},
			"name_prefix": map[string]interface{}{
				"type":        "string",
				"description": "Prefix used when generated names are needed (default: a2gent-local).",
			},
			"start_port": map[string]interface{}{
				"type":        "integer",
				"description": "Optional starting host port for sequential port assignment when agent host_port is omitted.",
			},
			"image": map[string]interface{}{
				"type":        "string",
				"description": "Optional Docker image (default: a2gent-brute:latest).",
			},
			"lm_studio_base_url": map[string]interface{}{
				"type":        "string",
				"description": "Optional LM Studio base URL override for all created agents.",
			},
			"session_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional parent session id label/env for all created agents.",
			},
			"project_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional project ID to mount as /workspace for all created agents.",
			},
			"project_mount_mode": map[string]interface{}{
				"type":        "string",
				"description": "Project mount mode when project_id is provided: ro (default) or rw.",
				"enum":        []string{"ro", "rw"},
			},
			"continue_on_error": map[string]interface{}{
				"type":        "boolean",
				"description": "When true (default), continue creating other agents even if one fails.",
			},
		},
		"required": []string{"agents"},
	}
}

func (t *createLocalDockerAgentsBulkTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p createLocalDockerAgentsBulkParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if len(p.Agents) == 0 {
		return &tools.Result{Success: false, Error: "agents must contain at least one item"}, nil
	}
	if len(p.Agents) > 100 {
		return &tools.Result{Success: false, Error: "agents list is too large (max 100)"}, nil
	}

	namePrefix := strings.TrimSpace(p.NamePrefix)
	if namePrefix == "" {
		namePrefix = "a2gent-local"
	}
	startPort := p.StartPort
	if startPort != 0 && (startPort < 1 || startPort > 65535) {
		return &tools.Result{Success: false, Error: "start_port must be between 1 and 65535 when provided"}, nil
	}
	continueOnError := true
	if p.ContinueOnError != nil {
		continueOnError = *p.ContinueOnError
	}
	timestampSuffix := time.Now().UnixMilli()

	created := make([]map[string]interface{}, 0, len(p.Agents))
	failures := make([]map[string]interface{}, 0)

	for i, spec := range p.Agents {
		hostPort := spec.HostPort
		if hostPort <= 0 && startPort > 0 {
			hostPort = startPort + i
		}

		name := strings.TrimSpace(spec.Name)
		if name == "" {
			fallbackToken := slugifyForDockerName(strings.TrimSpace(spec.AgentKind))
			if fallbackToken == "" {
				fallbackToken = fmt.Sprintf("agent-%d", i+1)
			}
			name = fmt.Sprintf("%s-%d-%d-%s", namePrefix, timestampSuffix, i+1, fallbackToken)
		}

		createReq := createLocalDockerAgentRequest{
			Name:             name,
			Image:            strings.TrimSpace(p.Image),
			HostPort:         hostPort,
			LMStudioBaseURL:  strings.TrimSpace(p.LMStudioBaseURL),
			AgentKind:        strings.TrimSpace(spec.AgentKind),
			SystemPrompt:     strings.TrimSpace(spec.SystemPrompt),
			SessionID:        strings.TrimSpace(p.SessionID),
			ProjectID:        strings.TrimSpace(p.ProjectID),
			ProjectMountMode: strings.TrimSpace(p.ProjectMountMode),
		}

		result, _, err := t.server.createLocalDockerAgent(ctx, createReq)
		if err != nil {
			failures = append(failures, map[string]interface{}{
				"index": i,
				"name":  name,
				"error": err.Error(),
			})
			if !continueOnError {
				break
			}
			continue
		}
		if result.Agent != nil {
			created = append(created, map[string]interface{}{
				"index": i,
				"agent": result.Agent,
			})
			continue
		}
		created = append(created, map[string]interface{}{
			"index": i,
			"agent": map[string]interface{}{
				"name":    result.Name,
				"status":  "started",
				"warning": result.Warning,
			},
		})
	}

	payload := map[string]interface{}{
		"success":       len(failures) == 0,
		"requested":     len(p.Agents),
		"created_count": len(created),
		"failed_count":  len(failures),
		"created":       created,
		"failures":      failures,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode tool output: %w", err)
	}

	return &tools.Result{
		Success: true,
		Output:  string(encoded),
		Metadata: map[string]interface{}{
			"requested":     len(p.Agents),
			"created_count": len(created),
			"failed_count":  len(failures),
		},
	}, nil
}

func slugifyForDockerName(value string) string {
	if value == "" {
		return ""
	}
	var out strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		isValid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if isValid {
			out.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			out.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(out.String(), "-")
	return slug
}

var _ tools.Tool = (*createLocalDockerAgentsBulkTool)(nil)
