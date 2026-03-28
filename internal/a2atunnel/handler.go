package a2atunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
	"github.com/google/uuid"
)

// A2A metadata keys stored on sessions created from inbound requests.
const (
	MetaA2AInbound         = "a2a_inbound"
	MetaA2ASourceAgentID   = "a2a_source_agent_id"
	MetaA2ASourceAgentName = "a2a_source_agent_name"
	MetaA2ASourceSessionID = "a2a_source_session_id"
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
	// inboundSubAgentID returns the sub-agent ID to assign to inbound sessions.
	// Called on each request so changes to settings take effect immediately.
	inboundSubAgentID func() string
}

// NewInboundHandler constructs an InboundHandler.
func NewInboundHandler(
	agentID string,
	sessionManager *session.Manager,
	agentFactory AgentRunnerBuilder,
	toolManagerFactory ToolManagerFactory,
	inboundProjectID func() string,
	inboundSubAgentID func() string,
) *InboundHandler {
	return &InboundHandler{
		agentID:            agentID,
		sessionManager:     sessionManager,
		agentFactory:       agentFactory,
		toolManagerFactory: toolManagerFactory,
		inboundProjectID:   inboundProjectID,
		inboundSubAgentID:  inboundSubAgentID,
	}
}

// Handle implements Handler. Blocks until the agent loop finishes.
func (h *InboundHandler) Handle(ctx context.Context, req *AgentRequest) ([]byte, error) {
	// 1. Decode the task payload.
	var p InboundPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		return nil, fmt.Errorf("invalid inbound payload: %w", err)
	}
	if len(p.Content) > 0 {
		if err := ValidateA2AContent(p.Content); err != nil {
			return nil, fmt.Errorf("invalid content payload: %w", err)
		}
		derivedTask, derivedImages := LegacyFromA2AContent(p.Content)
		if strings.TrimSpace(p.Task) == "" {
			p.Task = derivedTask
		}
		if len(p.Images) == 0 {
			p.Images = derivedImages
		}
	}
	if p.Sender != nil {
		if strings.TrimSpace(p.SourceAgentID) == "" {
			p.SourceAgentID = strings.TrimSpace(p.Sender.AgentID)
		}
		if strings.TrimSpace(p.SourceAgentName) == "" {
			p.SourceAgentName = strings.TrimSpace(p.Sender.Name)
		}
	}
	if strings.TrimSpace(p.Task) == "" && len(p.Images) == 0 {
		return nil, fmt.Errorf("inbound payload missing both 'task' and 'images'")
	}
	if err := ValidateA2AImages(p.Images); err != nil {
		return nil, fmt.Errorf("invalid images payload: %w", err)
	}
	incomingImages := a2aImagesToSession(p.Images)

	// 2. Resolve base project (dynamic — reads current settings).
	projectID := h.inboundProjectID()
	subAgentID := ""
	if h.inboundSubAgentID != nil {
		subAgentID = strings.TrimSpace(h.inboundSubAgentID())
	}

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
	if strings.TrimSpace(p.SourceSessionID) != "" {
		sess.Metadata[MetaA2ASourceSessionID] = strings.TrimSpace(p.SourceSessionID)
	}
	sess.Metadata[MetaA2ARequestID] = req.RequestID
	if strings.TrimSpace(p.ConversationID) != "" {
		sess.Metadata[MetaA2AConversationID] = strings.TrimSpace(p.ConversationID)
	}
	if projectID != "" {
		sess.ProjectID = &projectID
	}
	if subAgentID != "" {
		sess.Metadata["sub_agent_id"] = subAgentID
	} else {
		delete(sess.Metadata, "sub_agent_id")
		delete(sess.Metadata, "sub_agent_name")
	}
	if pending, _ := h.sessionManager.GetPendingQuestion(sess.ID); pending != nil && sess.Status == session.StatusInputRequired {
		if len(incomingImages) > 0 {
			return nil, fmt.Errorf("agent is waiting for text input; image attachments are not supported for pending questions")
		}
		if err := h.sessionManager.AnswerQuestion(sess.ID, p.Task); err != nil {
			return nil, fmt.Errorf("failed to answer pending question: %w", err)
		}
		reloaded, reloadErr := h.sessionManager.Get(sess.ID)
		if reloadErr == nil && reloaded != nil {
			sess = reloaded
		}
	} else {
		sess.AddUserMessageWithImages(strings.TrimSpace(p.Task), incomingImages)
		sess.SetStatus(session.StatusRunning)
	}
	beforeRunCount := len(sess.Messages)
	if err := h.sessionManager.Save(sess); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	// 4. Build an agent scoped to this session and run the loop.
	toolManager := h.toolManagerFactory(sess)
	ag, err := h.agentFactory(ctx, sess, toolManager, inboundPromptForRouting(p.Task, len(incomingImages)))
	if err != nil {
		return nil, fmt.Errorf("failed to configure inbound execution target: %w", err)
	}

	result, _, runErr := ag.Run(ctx, sess, strings.TrimSpace(p.Task))
	if runErr != nil {
		return nil, fmt.Errorf("agent run failed: %w", runErr)
	}
	if sess.Status == session.StatusInputRequired {
		if question, qErr := h.sessionManager.GetPendingQuestion(sess.ID); qErr == nil && question != nil {
			text := renderPendingQuestion(question)
			if strings.TrimSpace(text) != "" {
				responseImages := []A2AImage(nil)
				content := BuildA2AContent(text, responseImages)
				out, encErr := json.Marshal(OutboundPayload{
					A2AVersion: A2ABridgeVersion,
					MessageID:  uuid.NewString(),
					Content:    content,
					Result:     text,
					Images:     responseImages,
				})
				if encErr != nil {
					return nil, fmt.Errorf("failed to encode question response: %w", encErr)
				}
				return out, nil
			}
			return nil, fmt.Errorf("agent requires user input")
		}
		return nil, fmt.Errorf("agent requires user input")
	}
	assistantContent, assistantImages := latestAssistantMessageSince(sess.Messages, beforeRunCount)
	if strings.TrimSpace(result) == "" {
		if fallback := strings.TrimSpace(assistantContent); fallback != "" {
			result = fallback
		} else {
			return nil, fmt.Errorf("agent run produced no assistant response")
		}
	}

	// 5. Encode result.
	responseImages := sessionImagesToA2A(assistantImages)
	out, err := json.Marshal(OutboundPayload{
		A2AVersion: A2ABridgeVersion,
		MessageID:  uuid.NewString(),
		Content:    BuildA2AContent(result, responseImages),
		Result:     result,
		Images:     responseImages,
	})
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
	sourceSessionID := strings.TrimSpace(p.SourceSessionID)
	if conversationID == "" && sourceSessionID == "" {
		sess, err := h.sessionManager.Create(h.agentID)
		if err != nil {
			return nil, fmt.Errorf("failed to create session: %w", err)
		}
		return sess, nil
	}

	sessions, err := h.sessionManager.List()
	if err == nil {
		var candidate *session.Session
		bestScore := -1
		for _, sess := range sessions {
			if sess == nil || sess.Metadata == nil {
				continue
			}
			if !metadataBool(sess.Metadata, MetaA2AInbound) {
				continue
			}
			metaSource := metadataString(sess.Metadata, MetaA2ASourceAgentID)
			metaConversation := metadataString(sess.Metadata, MetaA2AConversationID)
			metaSourceSession := metadataString(sess.Metadata, MetaA2ASourceSessionID)
			score, matched := inboundSessionMatchScore(conversationID, sourceAgentID, sourceSessionID, metaConversation, metaSource, metaSourceSession)
			if !matched {
				continue
			}
			if score > bestScore || (score == bestScore && (candidate == nil || sess.UpdatedAt.After(candidate.UpdatedAt))) {
				candidate = sess
				bestScore = score
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

func inboundSessionMatchScore(
	conversationID string,
	sourceAgentID string,
	sourceSessionID string,
	metaConversation string,
	metaSource string,
	metaSourceSession string,
) (int, bool) {
	if conversationID != "" && metaConversation == conversationID {
		score := 100
		if sourceAgentID != "" && metaSource == sourceAgentID {
			score += 10
		}
		if sourceSessionID != "" && metaSourceSession == sourceSessionID {
			score += 5
		}
		return score, true
	}
	if sourceSessionID != "" && metaSourceSession == sourceSessionID {
		score := 60
		if sourceAgentID != "" && metaSource == sourceAgentID {
			score += 10
		}
		return score, true
	}
	return 0, false
}

func metadataString(metadata map[string]interface{}, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if asString, ok := value.(string); ok {
		return strings.TrimSpace(asString)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func metadataBool(metadata map[string]interface{}, key string) bool {
	switch strings.ToLower(metadataString(metadata, key)) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

func latestAssistantMessageContentSince(messages []session.Message, start int) string {
	content, _ := latestAssistantMessageSince(messages, start)
	return content
}

func latestAssistantMessageSince(messages []session.Message, start int) (string, []session.ImageAttachment) {
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
		if content != "" || len(messages[i].Images) > 0 {
			images := make([]session.ImageAttachment, len(messages[i].Images))
			copy(images, messages[i].Images)
			return content, images
		}
	}
	return "", nil
}

func inboundPromptForRouting(text string, imageCount int) string {
	trimmed := strings.TrimSpace(text)
	if trimmed != "" {
		return trimmed
	}
	if imageCount > 0 {
		return fmt.Sprintf("[User sent %d image(s)]", imageCount)
	}
	return ""
}

func a2aImagesToSession(images []A2AImage) []session.ImageAttachment {
	if len(images) == 0 {
		return nil
	}
	out := make([]session.ImageAttachment, 0, len(images))
	for _, img := range images {
		out = append(out, session.ImageAttachment{
			Name:       strings.TrimSpace(img.Name),
			MediaType:  strings.TrimSpace(img.MediaType),
			DataBase64: strings.TrimSpace(img.DataBase64),
			URL:        strings.TrimSpace(img.URL),
		})
	}
	return out
}

func sessionImagesToA2A(images []session.ImageAttachment) []A2AImage {
	if len(images) == 0 {
		return nil
	}
	out := make([]A2AImage, 0, len(images))
	for _, img := range images {
		out = append(out, A2AImage{
			Name:       strings.TrimSpace(img.Name),
			MediaType:  strings.TrimSpace(img.MediaType),
			DataBase64: strings.TrimSpace(img.DataBase64),
			URL:        strings.TrimSpace(img.URL),
		})
	}
	return out
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
