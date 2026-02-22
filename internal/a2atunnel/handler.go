package a2atunnel

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

// A2A metadata keys stored on sessions created from inbound requests.
const (
	MetaA2AInbound         = "a2a_inbound"
	MetaA2ASourceAgentID   = "a2a_source_agent_id"
	MetaA2ASourceAgentName = "a2a_source_agent_name"
	MetaA2ARequestID       = "a2a_request_id"
)

// AgentRunnerFactory creates an *agent.Agent for a given tool manager.
// The closure captures the llm.Client, Config, and session.Manager.
type AgentRunnerFactory func(toolManager *tools.Manager) *agent.Agent

// ToolManagerFactory creates a tool manager scoped to a session's project.
type ToolManagerFactory func(sess *session.Session) *tools.Manager

// InboundHandler implements Handler. It creates a new session per inbound
// A2A request, runs the agent loop synchronously, and returns the result.
// Multiple calls may run concurrently — it is goroutine-safe.
type InboundHandler struct {
	agentID            string
	sessionManager     *session.Manager
	agentFactory       AgentRunnerFactory
	toolManagerFactory ToolManagerFactory
	// inboundProjectID returns the project ID to assign to inbound sessions.
	// Called on each request so changes to settings take effect immediately.
	inboundProjectID func() string
}

// NewInboundHandler constructs an InboundHandler.
func NewInboundHandler(
	agentID string,
	sessionManager *session.Manager,
	agentFactory AgentRunnerFactory,
	toolManagerFactory ToolManagerFactory,
	inboundProjectID func() string,
) *InboundHandler {
	return &InboundHandler{
		agentID:            agentID,
		sessionManager:     sessionManager,
		agentFactory:       agentFactory,
		toolManagerFactory: toolManagerFactory,
		inboundProjectID:   inboundProjectID,
	}
}

// Handle implements Handler. Blocks until the agent loop finishes.
func (h *InboundHandler) Handle(ctx context.Context, req *AgentRequest) ([]byte, error) {
	// 1. Decode the task payload.
	var p InboundPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		return nil, fmt.Errorf("invalid inbound payload: %w", err)
	}
	if p.Task == "" {
		return nil, fmt.Errorf("inbound payload missing 'task'")
	}

	// 2. Resolve base project (dynamic — reads current settings).
	projectID := h.inboundProjectID()

	// 3. Create a session stamped with A2A origin metadata.
	sess, err := h.sessionManager.Create(h.agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	sess.Metadata[MetaA2AInbound] = true
	sess.Metadata[MetaA2ASourceAgentID] = p.SourceAgentID
	sess.Metadata[MetaA2ASourceAgentName] = p.SourceAgentName
	sess.Metadata[MetaA2ARequestID] = req.RequestID
	if projectID != "" {
		sess.ProjectID = &projectID
	}
	sess.AddUserMessage(p.Task)
	if err := h.sessionManager.Save(sess); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	// 4. Build an agent scoped to this session and run the loop.
	toolManager := h.toolManagerFactory(sess)
	ag := h.agentFactory(toolManager)

	result, _, runErr := ag.Run(ctx, sess, p.Task)
	if runErr != nil {
		return nil, fmt.Errorf("agent run failed: %w", runErr)
	}

	// 5. Encode result.
	out, err := json.Marshal(OutboundPayload{Result: result})
	if err != nil {
		return nil, fmt.Errorf("failed to encode response: %w", err)
	}
	return out, nil
}

// compile-time assertion that InboundHandler satisfies Handler.
var _ Handler = (*InboundHandler)(nil)
