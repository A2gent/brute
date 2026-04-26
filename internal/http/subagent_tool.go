package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
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
	return `Delegate a task to a configured sub-agent. The sub-agent runs in its own session with its own context window, using the provider and tools configured for it. Use this to offload well-scoped tasks and reduce context usage in your main session.

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

	// Helper to build error result with sub-agent metadata always included
	saErrorResult := func(errMsg string) *tools.Result {
		return &tools.Result{
			Success: false,
			Error:   errMsg,
			Metadata: map[string]interface{}{
				"sub_agent_name": sa.Name,
				"sub_agent_id":   sa.ID,
			},
		}
	}

	// Get parent session ID from context
	parentSessionID, _ := ctx.Value("session_id").(string)

	// Create child session
	childSess, err := t.server.sessionManager.Create("subagent")
	if err != nil {
		return saErrorResult("failed to create child session: " + err.Error()), nil
	}

	// Set parent ID
	if parentSessionID != "" {
		childSess.ParentID = &parentSessionID
	}

	// Set sub-agent metadata
	if childSess.Metadata == nil {
		childSess.Metadata = make(map[string]interface{})
	}
	childSess.Metadata["sub_agent_id"] = sa.ID
	childSess.Metadata["sub_agent_name"] = sa.Name

	// Resolve provider/model from sub-agent config
	providerType := config.ProviderType(config.NormalizeProviderRef(sa.Provider))
	if providerType == "" {
		providerType = config.ProviderType(config.NormalizeProviderRef(t.server.config.ActiveProvider))
	}
	model := strings.TrimSpace(sa.Model)
	if model == "" {
		model = t.server.resolveModelForProvider(providerType)
	}

	childSess.Metadata["provider"] = string(providerType)
	childSess.Metadata["model"] = model

	// Also inherit project from parent session
	if parentSessionID != "" {
		parentSess, parentErr := t.server.sessionManager.Get(parentSessionID)
		if parentErr == nil && parentSess.ProjectID != nil {
			childSess.ProjectID = parentSess.ProjectID
		}
	}

	// Add delegated task as a user-role message for provider compatibility, with
	// metadata so the UI can show that this came from a parent agent.
	childSess.AddUserMessageWithImagesAndMetadata(task, nil, map[string]interface{}{
		"internal_handoff":  true,
		"handoff_kind":      "subagent_delegation",
		"parent_session_id": parentSessionID,
		"sub_agent_id":      sa.ID,
		"sub_agent_name":    sa.Name,
	})
	if err := t.server.sessionManager.Save(childSess); err != nil {
		logging.Warn("Failed to save child session with initial task: %v", err)
	}

	// Resolve execution target
	target, err := t.server.resolveExecutionTarget(ctx, providerType, model, task, childSess)
	if err != nil {
		childSess.SetStatus(session.StatusFailed)
		t.server.sessionManager.Save(childSess)
		return saErrorResult("failed to resolve sub-agent provider: " + err.Error()), nil
	}

	// Build filtered tool manager
	toolMgr := t.server.buildSubAgentToolManager(childSess, sa.EnabledTools)

	// Build system prompt from configured instruction blocks (falls back to hardcoded)
	snapshot := t.server.composeSubAgentSystemPromptSnapshot(sa, childSess)
	var systemPrompt string
	if snapshot != nil && strings.TrimSpace(snapshot.CombinedPrompt) != "" {
		systemPrompt = snapshot.CombinedPrompt
	} else {
		systemPrompt = t.buildSubAgentSystemPrompt(sa.Name)
	}

	agentConfig := agent.Config{
		Name:          "subagent-" + sa.Name,
		Model:         target.Model,
		SystemPrompt:  systemPrompt,
		MaxSteps:      30, // Sub-agents get fewer steps
		Temperature:   t.server.config.Temperature,
		ContextWindow: target.ContextWindow,
	}

	ag := agent.New(agentConfig, target.Client, toolMgr, t.server.sessionManager)

	logging.Info("Sub-agent delegation started: parent=%s child=%s sub_agent=%s task=%s",
		parentSessionID, childSess.ID, sa.Name, truncateForLog(task, 100))

	content, usage, err := ag.Run(ctx, childSess, task)
	if err != nil {
		childSess.SetStatus(session.StatusFailed)
		t.server.sessionManager.Save(childSess)
		return saErrorResult(fmt.Sprintf("sub-agent '%s' failed: %s", sa.Name, err.Error())), nil
	}

	logging.Info("Sub-agent delegation completed: child=%s input_tokens=%d output_tokens=%d",
		childSess.ID, usage.InputTokens, usage.OutputTokens)

	// Truncate response to avoid bloating parent context
	responseText := strings.TrimSpace(content)
	if len(responseText) > 4000 {
		responseText = responseText[:4000] + "\n...(truncated)"
	}

	payload := map[string]interface{}{
		"success":          true,
		"child_session_id": childSess.ID,
		"sub_agent_name":   sa.Name,
		"response":         responseText,
		"usage": map[string]int{
			"input_tokens":  usage.InputTokens,
			"output_tokens": usage.OutputTokens,
		},
	}

	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode tool output: %w", err)
	}
	return &tools.Result{
		Success: true,
		Output:  string(body),
		Metadata: map[string]interface{}{
			"child_session_id": childSess.ID,
			"sub_agent_name":   sa.Name,
		},
	}, nil
}

func (t *delegateToSubAgentTool) buildSubAgentSystemPrompt(name string) string {
	return fmt.Sprintf(`You are a sub-agent named "%s". You have been delegated a specific task by the main agent.

Guidelines:
- Focus exclusively on completing the delegated task
- Be concise and efficient — your output will be returned to the main agent
- Use available tools to accomplish the task
- If the task is unclear, do your best to complete it based on context
- When done, provide a clear summary of what you accomplished`, name)
}

func (s *Server) buildSubAgentToolManager(sess *session.Session, enabledTools []string) *tools.Manager {
	workDir := s.resolveSessionWorkDir(sess)
	defaultDir := strings.TrimSpace(s.config.WorkDir)
	if defaultDir == "" {
		defaultDir = "."
	}

	var manager *tools.Manager
	if workDir == defaultDir {
		manager = s.toolManager.Clone()
	} else {
		manager = tools.NewManager(workDir)
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

	// Sub-agents should not be able to delegate to other sub-agents (prevent recursion)
	manager.Unregister("delegate_to_subagent")

	return manager
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

var _ tools.Tool = (*delegateToSubAgentTool)(nil)
