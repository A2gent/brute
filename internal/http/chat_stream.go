// chat_stream.go keeps streaming chat behavior isolated from the former monolithic server.go.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strings"
	"sync"
	"time"
)

func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	images, err := normalizeIncomingImages(req.Images)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid images payload: "+err.Error())
		return
	}

	if strings.TrimSpace(req.Message) == "" && len(images) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "Message or images are required")
		return
	}

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}
	defer s.queueTelegramSessionMessageSync(sess.ID)
	if sess.Status == session.StatusQueued && sessionIsSerialQueuedAutoRun(sess) {
		s.triggerSerialSessionQueueForSession(sess)
		s.errorResponse(w, http.StatusConflict, "Session is queued for serial execution and will start automatically")
		return
	}

	lastUserMsg := ""
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role == "user" {
			lastUserMsg = sess.Messages[i].Content
			break
		}
	}
	if lastUserMsg != req.Message || !sameMessageImages(lastUserMessageImages(sess), images) {
		sess.AddUserMessageWithImages(req.Message, images)
	}
	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update session: "+err.Error())
		return
	}

	runCtx, cancelRun := context.WithCancel(s.sessionRunParentContext())
	runID := s.registerActiveSessionRun(sessionID, cancelRun)
	defer func() {
		cancelRun()
		s.unregisterActiveSessionRun(sessionID, runID)
	}()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "Streaming is not supported by the server")
		return
	}

	var streamWriteMu sync.Mutex
	streamWritable := true
	writeEvent := func(event ChatStreamEvent) bool {
		streamWriteMu.Lock()
		defer streamWriteMu.Unlock()
		if !streamWritable {
			return false
		}
		if err := json.NewEncoder(w).Encode(event); err != nil {
			streamWritable = false
			return false
		}
		flusher.Flush()
		return true
	}

	streamConnected := writeEvent(ChatStreamEvent{Type: "status", Status: string(sess.Status)})
	var heartbeatDone chan struct{}
	if streamConnected {
		heartbeatDone = make(chan struct{})
		defer close(heartbeatDone)
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if !writeEvent(ChatStreamEvent{Type: "heartbeat"}) {
						return
					}
				case <-heartbeatDone:
					return
				}
			}
		}()
	} else {
		logging.Warn("Chat stream disconnected before initial status event was delivered; continuing run without live streaming: session=%s", sess.ID)
	}

	if s.hasRunnableWorkflow(sess) {
		content, usage, runErr := s.runWorkflowSession(runCtx, sess, req.Message, writeEvent)
		if runErr != nil {
			if isCancellationError(runErr) {
				sess.SetStatus(session.StatusPaused)
				_ = s.sessionManager.Save(sess)
				_ = writeEvent(ChatStreamEvent{
					Type:     "error",
					Error:    "Request was canceled before completion",
					Status:   string(sess.Status),
					Messages: s.messagesToResponse(sess.Messages),
				})
				return
			}
			sess.AddAssistantMessage(fmt.Sprintf("Workflow failed: %s", runErr.Error()), nil)
			sess.SetStatus(session.StatusFailed)
			_ = s.sessionManager.Save(sess)
			s.refreshSessionSummaryWithPrompt(runCtx, sess)
			s.triggerSerialSessionQueueIfTerminal(sess)
			_ = writeEvent(ChatStreamEvent{
				Type:     "error",
				Error:    "Workflow error: " + runErr.Error(),
				Status:   string(sess.Status),
				Messages: s.messagesToResponse(sess.Messages),
			})
			return
		}
		sess.AddAssistantMessage(content, nil)
		sess.SetStatus(workflowSessionStatus(sess))
		if saveErr := s.sessionManager.Save(sess); saveErr != nil {
			_ = writeEvent(ChatStreamEvent{
				Type:     "error",
				Error:    "Failed to save workflow response: " + saveErr.Error(),
				Status:   string(sess.Status),
				Messages: s.messagesToResponse(sess.Messages),
			})
			return
		}
		s.triggerSerialSessionQueueIfTerminal(sess)
		_ = writeEvent(ChatStreamEvent{
			Type:     "done",
			Content:  content,
			Messages: s.messagesToResponse(sess.Messages),
			Status:   string(sess.Status),
			Usage: &UsageResponse{
				InputTokens:  usage.InputTokens,
				OutputTokens: usage.OutputTokens,
			},
		})
		return
	}

	providerType := s.resolveSessionProviderType(sess)
	model := s.resolveSessionModel(sess, providerType)
	routingPrompt := messageForRouting(req.Message, len(images))
	target, err := s.resolveExecutionTarget(runCtx, providerType, model, routingPrompt, sess)
	if err != nil {
		sess.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", err.Error()), nil)
		sess.SetStatus(session.StatusFailed)
		s.sessionManager.Save(sess)
		s.triggerSerialSessionQueueIfTerminal(sess)
		_ = writeEvent(ChatStreamEvent{
			Type:     "error",
			Error:    "Provider configuration error: " + err.Error(),
			Status:   string(sess.Status),
			Messages: s.messagesToResponse(sess.Messages),
		})
		return
	}
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

	content, usage, err := ag.RunWithEvents(runCtx, sess, req.Message, func(ev agent.Event) {
		switch ev.Type {
		case agent.EventAssistantDelta:
			_ = writeEvent(ChatStreamEvent{
				Type:  "assistant_delta",
				Delta: ev.Delta,
			})
		case agent.EventToolExecuting:
			toolCalls := make([]StreamToolCallEvent, len(ev.ToolCalls))
			for i, tc := range ev.ToolCalls {
				toolCalls[i] = StreamToolCallEvent{
					ID:               tc.ID,
					Name:             tc.Name,
					Input:            json.RawMessage(tc.Input),
					ThoughtSignature: tc.ThoughtSignature,
				}
			}
			_ = writeEvent(ChatStreamEvent{
				Type:      "tool_executing",
				Step:      ev.Step,
				Message:   streamLastMessageResponse(s, sess),
				ToolCalls: toolCalls,
			})
		case agent.EventToolProgress:
			if ev.ToolProgress == nil {
				return
			}
			_ = writeEvent(ChatStreamEvent{
				Type: "tool_progress",
				Step: ev.Step,
				ToolProgress: &StreamToolProgressEvent{
					ToolCallID: ev.ToolProgress.ToolCallID,
					ToolName:   ev.ToolProgress.ToolName,
					Status:     ev.ToolProgress.Status,
					Content:    ev.ToolProgress.Content,
					Metadata:   ev.ToolProgress.Metadata,
				},
			})
		case agent.EventToolCompleted:
			event := ChatStreamEvent{
				Type:   "tool_completed",
				Step:   ev.Step,
				Status: string(sess.Status),
			}
			if len(sess.Messages) > 0 {
				msg := s.messageToResponse(sess.Messages[len(sess.Messages)-1])
				event.Message = &msg
			}
			_ = writeEvent(event)
		case agent.EventStepCompleted:
			_ = writeEvent(ChatStreamEvent{
				Type: "step_completed",
				Step: ev.Step,
			})
		case agent.EventProviderTrace:
			if ev.Provider == nil {
				return
			}
			s.applyProviderTraceToSession(sess, target.ProviderType, ev.Provider)
			_ = writeEvent(ChatStreamEvent{
				Type: "provider_trace",
				Step: ev.Step,
				Provider: &StreamProviderEvent{
					Provider:      ev.Provider.Provider,
					Model:         ev.Provider.Model,
					Attempt:       ev.Provider.Attempt,
					MaxAttempts:   ev.Provider.MaxAttempts,
					NodeIndex:     ev.Provider.NodeIndex,
					TotalNodes:    ev.Provider.TotalNodes,
					Phase:         ev.Provider.Phase,
					Reason:        ev.Provider.Reason,
					FallbackTo:    ev.Provider.FallbackTo,
					FallbackModel: ev.Provider.FallbackModel,
					Recovered:     ev.Provider.Recovered,
				},
			})
		}
	})

	if err != nil {
		if isCancellationError(err) {
			sess.SetStatus(session.StatusPaused)
			s.sessionManager.Save(sess)
			_ = writeEvent(ChatStreamEvent{
				Type:     "error",
				Error:    "Request was canceled before completion.",
				Status:   string(sess.Status),
				Messages: s.messagesToResponse(sess.Messages),
			})
			return
		}
		adaptedErr := s.adaptProviderErrorMessage(target.ProviderType, err)
		addRequestFailedAssistantMessage(sess, adaptedErr)
		sess.SetStatus(session.StatusFailed)
		s.sessionManager.Save(sess)
		s.triggerSerialSessionQueueIfTerminal(sess)
		_ = writeEvent(ChatStreamEvent{
			Type:     "error",
			Error:    "Agent error: " + adaptedErr.Error(),
			Status:   string(sess.Status),
			Messages: s.messagesToResponse(sess.Messages),
		})
		return
	}

	if event := s.inputRequiredStreamEvent(sess); event != nil {
		_ = writeEvent(*event)
	}

	s.refreshSessionSummaryWithPrompt(runCtx, sess)
	s.triggerSerialSessionQueueIfTerminal(sess)
	_ = writeEvent(ChatStreamEvent{
		Type:     "done",
		Content:  content,
		Messages: s.messagesToResponse(sess.Messages),
		Status:   string(sess.Status),
		Usage: &UsageResponse{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
		},
	})
}

func (s *Server) inputRequiredStreamEvent(sess *session.Session) *ChatStreamEvent {
	if s == nil || sess == nil || sess.Status != session.StatusInputRequired {
		return nil
	}

	var pending *session.QuestionData
	if s.sessionManager != nil {
		if question, err := s.sessionManager.GetPendingQuestion(sess.ID); err == nil {
			pending = question
		}
	}

	return &ChatStreamEvent{
		Type:     "input_required",
		Status:   string(sess.Status),
		Messages: s.messagesToResponse(sess.Messages),
		Question: pending,
	}
}

func streamLastMessageResponse(s *Server, sess *session.Session) *MessageResponse {
	if sess == nil || len(sess.Messages) == 0 {
		return nil
	}
	msg := s.messageToResponse(sess.Messages[len(sess.Messages)-1])
	return &msg
}
