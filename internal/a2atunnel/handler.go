package a2atunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	MetaA2AConversationID  = "a2a_conversation_id"
)

// AgentRunnerBuilder creates an *agent.Agent configured for the request.
// It can resolve provider/model per prompt and session metadata.
type AgentRunnerBuilder func(ctx context.Context, sess *session.Session, toolManager *tools.Manager, userPrompt string) (*agent.Agent, error)

// ToolManagerFactory creates a tool manager scoped to a session's project.
type ToolManagerFactory func(sess *session.Session) *tools.Manager

// InboundHandler implements Handler. It creates a new session per inbound
// A2A request, runs the agent loop synchronously, and returns the result.
// Multiple calls may run concurrently — it is goroutine-safe.
type InboundHandler struct {
	agentID            string
	sessionManager     *session.Manager
	agentFactory       AgentRunnerBuilder
	toolManagerFactory ToolManagerFactory
	// inboundProjectID returns the project ID to assign to inbound sessions.
	// Called on each request so changes to settings take effect immediately.
	inboundProjectID func() string
}

// NewInboundHandler constructs an InboundHandler.
func NewInboundHandler(
	agentID string,
	sessionManager *session.Manager,
	agentFactory AgentRunnerBuilder,
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

	// 3. Resolve or create a session stamped with A2A origin metadata.
	sess, err := h.resolveSession(p)
	if err != nil {
		return nil, err
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	sess.Metadata[MetaA2AInbound] = true
	sess.Metadata[MetaA2ASourceAgentID] = p.SourceAgentID
	sess.Metadata[MetaA2ASourceAgentName] = p.SourceAgentName
	sess.Metadata[MetaA2ARequestID] = req.RequestID
	if strings.TrimSpace(p.ConversationID) != "" {
		sess.Metadata[MetaA2AConversationID] = strings.TrimSpace(p.ConversationID)
	}
	if projectID != "" {
		sess.ProjectID = &projectID
	}
	if pending, _ := h.sessionManager.GetPendingQuestion(sess.ID); pending != nil && sess.Status == session.StatusInputRequired {
		if err := h.sessionManager.AnswerQuestion(sess.ID, p.Task); err != nil {
			return nil, fmt.Errorf("failed to answer pending question: %w", err)
		}
		reloaded, reloadErr := h.sessionManager.Get(sess.ID)
		if reloadErr == nil && reloaded != nil {
			sess = reloaded
		}
	} else {
		sess.AddUserMessage(p.Task)
		sess.SetStatus(session.StatusRunning)
	}
	beforeRunCount := len(sess.Messages)
	if err := h.sessionManager.Save(sess); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	// 4. Build an agent scoped to this session and run the loop.
	toolManager := h.toolManagerFactory(sess)
	ag, err := h.agentFactory(ctx, sess, toolManager, p.Task)
	if err != nil {
		return nil, fmt.Errorf("failed to configure inbound execution target: %w", err)
	}

	result, _, runErr := ag.Run(ctx, sess, p.Task)
	if runErr != nil {
		return nil, fmt.Errorf("agent run failed: %w", runErr)
	}
	if sess.Status == session.StatusInputRequired {
		if question, qErr := h.sessionManager.GetPendingQuestion(sess.ID); qErr == nil && question != nil {
			text := renderPendingQuestion(question)
			if strings.TrimSpace(text) != "" {
				out, encErr := json.Marshal(OutboundPayload{Result: text})
				if encErr != nil {
					return nil, fmt.Errorf("failed to encode question response: %w", encErr)
				}
				return out, nil
			}
			return nil, fmt.Errorf("agent requires user input")
		}
		return nil, fmt.Errorf("agent requires user input")
	}
	if strings.TrimSpace(result) == "" {
		if fallback := latestAssistantMessageContentSince(sess.Messages, beforeRunCount); fallback != "" {
			result = fallback
		} else {
			return nil, fmt.Errorf("agent run produced no assistant response")
		}
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

func (h *InboundHandler) resolveSession(p InboundPayload) (*session.Session, error) {
	conversationID := strings.TrimSpace(p.ConversationID)
	sourceAgentID := strings.TrimSpace(p.SourceAgentID)
	if conversationID == "" || sourceAgentID == "" {
		sess, err := h.sessionManager.Create(h.agentID)
		if err != nil {
			return nil, fmt.Errorf("failed to create session: %w", err)
		}
		return sess, nil
	}

	sessions, err := h.sessionManager.List()
	if err == nil {
		var candidate *session.Session
		for _, sess := range sessions {
			if sess == nil || sess.Metadata == nil {
				continue
			}
			inbound, _ := sess.Metadata[MetaA2AInbound].(bool)
			if !inbound {
				continue
			}
			metaSource, _ := sess.Metadata[MetaA2ASourceAgentID].(string)
			metaConversation, _ := sess.Metadata[MetaA2AConversationID].(string)
			if strings.TrimSpace(metaSource) == sourceAgentID && strings.TrimSpace(metaConversation) == conversationID {
				if candidate == nil || sess.UpdatedAt.After(candidate.UpdatedAt) {
					candidate = sess
				}
			}
		}
		if candidate != nil {
			full, getErr := h.sessionManager.Get(candidate.ID)
			if getErr == nil && full != nil {
				return full, nil
			}
			return candidate, nil
		}
	}

	sess, err := h.sessionManager.Create(h.agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	return sess, nil
}

func latestAssistantMessageContentSince(messages []session.Message, start int) string {
	if start < 0 {
		start = 0
	}
	if start > len(messages) {
		start = len(messages)
	}
	for i := len(messages) - 1; i >= start; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		content := strings.TrimSpace(messages[i].Content)
		if content != "" {
			return content
		}
	}
	return ""
}

func renderPendingQuestion(q *session.QuestionData) string {
	if q == nil {
		return ""
	}
	var b strings.Builder
	question := strings.TrimSpace(q.Question)
	if question == "" {
		question = "I need your input to continue."
	}
	b.WriteString(question)
	if len(q.Options) > 0 {
		b.WriteString("\n\nOptions:\n")
		for _, opt := range q.Options {
			label := strings.TrimSpace(opt.Label)
			if label == "" {
				continue
			}
			desc := strings.TrimSpace(opt.Description)
			if desc != "" {
				b.WriteString("- ")
				b.WriteString(label)
				b.WriteString(": ")
				b.WriteString(desc)
				b.WriteString("\n")
				continue
			}
			b.WriteString("- ")
			b.WriteString(label)
			b.WriteString("\n")
		}
	}
	if q.Custom {
		b.WriteString("\nYou can also type your own answer.")
	}
	return strings.TrimSpace(b.String())
}
