package http

import (
	"context"
	"errors"
	"fmt"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

var errSessionProviderConfiguration = errors.New("session provider configuration")

type sessionRunResult struct {
	Content      string
	Usage        llm.TokenUsage
	Workflow     bool
	DirectAgent  bool
	Status       string
	ProviderType config.ProviderType
}

type sessionProviderConfigError struct {
	err error
}

func (e *sessionProviderConfigError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *sessionProviderConfigError) Unwrap() error {
	return errSessionProviderConfiguration
}

func (s *Server) runSessionWithoutStreaming(ctx context.Context, sess *session.Session, userMessage string) (sessionRunResult, error) {
	result := sessionRunResult{}
	publishEvent := func(event ChatStreamEvent) {
		s.publishSessionEvent(sess.ID, event)
	}
	if s.hasDirectAgentTarget(sess) {
		result.DirectAgent = true
		chatResp, err := s.runDirectAgentSession(ctx, sess, userMessage, publishEvent)
		result.Content = chatResp.Content
		result.Usage = llm.TokenUsage{InputTokens: chatResp.Usage.InputTokens, OutputTokens: chatResp.Usage.OutputTokens}
		result.Status = chatResp.Status
		return result, err
	}

	if s.hasRunnableWorkflow(sess) {
		result.Workflow = true
		content, usage, err := s.runWorkflowSession(ctx, sess, userMessage, func(event ChatStreamEvent) bool {
			publishEvent(event)
			return true
		})
		result.Content = content
		result.Usage = usage
		return result, err
	}

	providerType := s.resolveSessionProviderType(sess)
	model := s.resolveSessionModel(sess, providerType)
	result.ProviderType = providerType
	routingPrompt := messageForRouting(userMessage, len(lastUserMessageImages(sess)))
	target, err := s.resolveExecutionTarget(ctx, providerType, model, routingPrompt, sess)
	if err != nil {
		return result, &sessionProviderConfigError{err: err}
	}
	result.ProviderType = target.ProviderType
	if setSessionRoutedProviderAndModel(sess, providerType, target.ProviderType, target.Model) {
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist session routed target metadata: %v", err)
		}
	}

	agentConfig := agent.Config{
		Name:                sess.AgentID,
		Provider:            string(target.ProviderType),
		Model:               target.Model,
		SystemPrompt:        s.buildSystemPromptForSession(sess),
		MaxSteps:            s.config.MaxSteps,
		Temperature:         s.config.Temperature,
		ContextWindow:       target.ContextWindow,
		UsePreviousResponse: target.StatefulResponses,
	}

	ag := s.newAgentFromConfig(agentConfig, target.Client, s.toolManagerForSession(sess))
	content, usage, err := ag.RunWithEvents(ctx, sess, userMessage, func(ev agent.Event) {
		if event, ok := s.agentEventToStreamEvent(sess, target.ProviderType, ev); ok {
			publishEvent(event)
		}
	})
	result.Content = content
	result.Usage = usage
	return result, err
}

func (s *Server) finalizeSessionRunWithoutStreaming(ctx context.Context, sess *session.Session, result sessionRunResult, runErr error) error {
	publishError := func(message string) {
		s.publishSessionEvent(sess.ID, ChatStreamEvent{
			Type:     "error",
			Error:    message,
			Status:   string(sess.Status),
			Messages: s.messagesToResponse(sess.Messages),
		})
	}
	if runErr != nil {
		if isCancellationError(runErr) {
			sess.SetStatus(session.StatusPaused)
			_ = s.sessionManager.Save(sess)
			publishError("Request was canceled before completion")
			return runErr
		}
		if errors.Is(runErr, errSessionProviderConfiguration) {
			sess.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", runErr.Error()), nil)
			sess.SetStatus(session.StatusFailed)
			_ = s.sessionManager.Save(sess)
			s.refreshSessionSummaryWithPrompt(ctx, sess)
			s.triggerSerialSessionQueueIfAdvanceable(sess)
			publishError("Provider configuration error: " + runErr.Error())
			return runErr
		}
		if result.DirectAgent {
			sess.AddAssistantMessage(fmt.Sprintf("Agent failed: %s", runErr.Error()), nil)
			sess.SetStatus(session.StatusFailed)
			_ = s.sessionManager.Save(sess)
			s.refreshSessionSummaryWithPrompt(ctx, sess)
			s.triggerSerialSessionQueueIfAdvanceable(sess)
			publishError("Agent error: " + runErr.Error())
			return runErr
		}
		if result.Workflow {
			sess.AddAssistantMessage(fmt.Sprintf("Workflow failed: %s", runErr.Error()), nil)
			sess.SetStatus(session.StatusFailed)
			_ = s.sessionManager.Save(sess)
			s.triggerSerialSessionQueueIfAdvanceable(sess)
			publishError("Workflow error: " + runErr.Error())
			return runErr
		}
		adaptedErr := s.adaptProviderErrorMessage(result.ProviderType, runErr)
		addRequestFailedAssistantMessage(sess, adaptedErr)
		sess.SetStatus(session.StatusFailed)
		_ = s.sessionManager.Save(sess)
		s.refreshSessionSummaryWithPrompt(ctx, sess)
		s.triggerSerialSessionQueueIfAdvanceable(sess)
		publishError("Agent error: " + adaptedErr.Error())
		return adaptedErr
	}

	if result.DirectAgent {
		sess.AddAssistantMessage(result.Content, nil)
		sess.SetStatus(session.StatusCompleted)
		if result.Status == string(session.StatusInputRequired) || result.Status == string(session.StatusPaused) || result.Status == string(session.StatusWaitingExternal) {
			sess.SetStatus(session.Status(result.Status))
		}
		if err := s.sessionManager.Save(sess); err != nil {
			return fmt.Errorf("failed to save agent response: %w", err)
		}
	}

	if result.Workflow {
		sess.AddAssistantMessage(result.Content, nil)
		sess.SetStatus(workflowSessionStatus(sess))
		if err := s.sessionManager.Save(sess); err != nil {
			return fmt.Errorf("failed to save workflow response: %w", err)
		}
	}

	s.refreshSessionSummaryWithPrompt(ctx, sess)
	s.triggerSerialSessionQueueIfAdvanceable(sess)
	if event := s.inputRequiredStreamEvent(sess); event != nil {
		s.publishSessionEvent(sess.ID, *event)
	}
	s.publishSessionEvent(sess.ID, ChatStreamEvent{
		Type:     "done",
		Content:  result.Content,
		Messages: s.messagesToResponse(sess.Messages),
		Status:   string(sess.Status),
		Usage: &UsageResponse{
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
		},
	})
	return nil
}

func (s *Server) sessionRunHTTPError(result sessionRunResult, runErr error) (int, string) {
	if isCancellationError(runErr) {
		return 409, "Request was canceled before completion"
	}
	if errors.Is(runErr, errSessionProviderConfiguration) {
		return 400, "Provider configuration error: " + runErr.Error()
	}
	if result.DirectAgent {
		return 500, "Agent error: " + runErr.Error()
	}
	if result.Workflow {
		return 500, "Workflow error: " + runErr.Error()
	}
	adaptedErr := s.adaptProviderErrorMessage(result.ProviderType, runErr)
	return 500, "Agent error: " + adaptedErr.Error()
}
