package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/filesearch"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

type delegateToSubAgentTool struct {
	server *Server
}

type delegateToSubAgentParams struct {
	SubAgentID string `json:"sub_agent_id"`
	Task       string `json:"task"`
}

func newDelegateToSubAgentTool(server *Server) *delegateToSubAgentTool {
	return &delegateToSubAgentTool{server: server}
}

func (t *delegateToSubAgentTool) Name() string {
	return "delegate_to_subagent"
}

func (t *delegateToSubAgentTool) Description() string {
	return `Compatibility alias for delegate_to_agent. Delegate a task to a configured agent by sub-agent ID; the agent runs in Docker with its configured provider, tools, project binding, and instructions.

Returns the sub-agent's response and the child session ID.`
}

func (t *delegateToSubAgentTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"sub_agent_id": map[string]interface{}{
				"type":        "string",
				"description": "ID of the sub-agent to delegate to. Use the list available in your system prompt or ask the user.",
			},
			"task": map[string]interface{}{
				"type":        "string",
				"description": "Clear, specific task description for the sub-agent to complete.",
			},
		},
		"required": []string{"sub_agent_id", "task"},
	}
}

func (t *delegateToSubAgentTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p delegateToSubAgentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	subAgentID := strings.TrimSpace(p.SubAgentID)
	task := strings.TrimSpace(p.Task)
	if subAgentID == "" {
		return &tools.Result{Success: false, Error: "sub_agent_id is required"}, nil
	}
	if task == "" {
		return &tools.Result{Success: false, Error: "task is required"}, nil
	}

	// Load sub-agent config
	sa, err := t.server.store.GetSubAgent(subAgentID)
	if err != nil {
		return &tools.Result{Success: false, Error: "sub-agent not found: " + err.Error()}, nil
	}

	return t.server.runSubAgentDockerDelegation(ctx, sa, task)
}

func (s *Server) buildSubAgentToolManager(sess *session.Session, enabledTools []string) *tools.Manager {
	workDir := s.resolveSessionWorkDir(sess)
	defaultDir := strings.TrimSpace(s.config.WorkDir)
	if defaultDir == "" {
		defaultDir = "."
	}

	indexingEnabled := s.resolveSessionFileIndexingEnabled(sess)
	indexingDiffers := indexingEnabled != filesearch.IndexingEnabled()

	var manager *tools.Manager
	if workDir == defaultDir && !indexingDiffers {
		manager = s.toolManager.Clone()
	} else {
		manager = tools.NewManagerWithOptions(workDir, &tools.ManagerOptions{FileIndexingEnabled: &indexingEnabled})
		// WHY: project-scoped sub-agents get a fresh manager for their workdir; it
		// must include integration-backed tools too, otherwise allowed tools like
		// youtube_transcript fail at runtime with "tool not found".
		integrationtools.Register(manager, s.store, s.speechClips, s.sessionManager)
		s.registerServerBackedTools(manager)
	}

	// If enabled_tools is specified, remove all tools NOT in the list
	if len(enabledTools) > 0 {
		allowed := make(map[string]struct{}, len(enabledTools))
		for _, name := range enabledTools {
			allowed[strings.TrimSpace(name)] = struct{}{}
		}
		// Also always allow the question tool and task progress
		allowed["question"] = struct{}{}
		allowed["session_task_progress"] = struct{}{}

		for _, def := range manager.GetDefinitions() {
			if _, ok := allowed[def.Name]; !ok {
				manager.Unregister(def.Name)
			}
		}
	}

	// Sub-agents should not be able to delegate to other agents (prevent recursion)
	manager.Unregister("delegate_to_subagent")
	manager.Unregister("delegate_to_agent")

	return manager
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

var _ tools.Tool = (*delegateToSubAgentTool)(nil)
