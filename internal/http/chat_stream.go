// chat_stream.go keeps streaming chat behavior isolated from the former monolithic server.go.
package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strings"
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

	leaseCtx, release, leaseErr := s.sessionManager.AcquireRun(s.sessionRunParentContext(), sessionID)
	if leaseErr != nil || s.activeSessionRunCount(sessionID) > 0 {
		if release != nil {
			release()
		}
		s.errorResponse(w, 409, "Session already has an active run")
		return
	}
	defer release()
	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}
	if t := session.CurrentProviderTransition(sess); t != nil && !t.Terminal() {
		s.errorResponse(w, 409, "Resolve the pending provider transition first")
		return
	}
	defer s.queueTelegramSessionMessageSync(sess.ID)
	defer s.closeBrowserPageIfTerminal(sess)
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
	s.publishSessionCatalog("session_updated", sess)

	runCtx, cancelRun := s.chatStreamRunContextWithParent(r.Context(), leaseCtx, sess)
	runID := s.registerActiveSessionRun(sessionID, cancelRun)
	defer func() {
		cancelRun()
		s.unregisterActiveSessionRun(sessionID, runID)
	}()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "Streaming is not supported by the server")
		return
	}

	streamWritable := true
	encoder := json.NewEncoder(w)
	stream := newChatStreamFlusher(func(event ChatStreamEvent) bool {
		s.publishSessionEvent(sessionID, event)
		if !streamWritable {
			return false
		}
		if err := encoder.Encode(event); err != nil {
			streamWritable = false
			return false
		}
		flusher.Flush()
		return true
	}, chatStreamFlushWindow, chatStreamFlushBytes)
	defer stream.Close()
	writeEvent := stream.Write

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

	if s.hasDirectAgentTarget(sess) {
		chatResp, runErr := s.runDirectAgentSession(runCtx, sess, req.Message, func(event ChatStreamEvent) {
			if event.Type != "done" && event.Type != "error" {
				_ = writeEvent(event)
			}
		})
		if runErr != nil {
			sess.AddAssistantMessage(fmt.Sprintf("Agent failed: %s", runErr.Error()), nil)
			sess.SetStatus(session.StatusFailed)
			_ = s.sessionManager.Save(sess)
			s.refreshSessionSummaryAsync(sess)
			s.triggerSerialSessionQueueIfAdvanceable(sess)
			_ = writeEvent(ChatStreamEvent{Type: "error", Error: "Agent error: " + runErr.Error(), Status: string(sess.Status), Messages: s.messagesToResponse(sess.Messages)})
			return
		}
		sess.AddAssistantMessage(chatResp.Content, nil)
		sess.SetStatus(session.StatusCompleted)
		if chatResp.Status == string(session.StatusInputRequired) || chatResp.Status == string(session.StatusPaused) || chatResp.Status == string(session.StatusWaitingExternal) {
			sess.SetStatus(session.Status(chatResp.Status))
		}
		if saveErr := s.sessionManager.Save(sess); saveErr != nil {
			_ = writeEvent(ChatStreamEvent{Type: "error", Error: "Failed to save agent response: " + saveErr.Error(), Status: string(sess.Status), Messages: s.messagesToResponse(sess.Messages)})
			return
		}
		s.refreshSessionSummaryAsync(sess)
		_ = writeEvent(ChatStreamEvent{Type: "done", Content: chatResp.Content, Messages: s.messagesToResponse(sess.Messages), Status: string(sess.Status), Usage: &chatResp.Usage})
		s.triggerSerialSessionQueueIfAdvanceable(sess)
		s.publishSessionCatalog("session_updated", sess)
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
		s.triggerSerialSessionQueueIfAdvanceable(sess)
		_ = writeEvent(ChatStreamEvent{
			Type:     "error",
			Error:    "Provider configuration error: " + err.Error(),
			Status:   string(sess.Status),
			Messages: s.messagesToResponse(sess.Messages),
		})
		return
	}
	if setSessionRoutedProviderAndModel(sess, providerType, target.ProviderType, target.Model, target.RoutingRule, target.RoutingReason) {
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist session routed target metadata: %v", err)
		}
	}
	if event := routerDecisionStreamEvent(providerType, target); event != nil {
		_ = writeEvent(*event)
	}

	agentConfig := s.agentConfigFromTarget(sess, target, s.buildSystemPromptForSession(sess), s.config.MaxSteps, s.config.Temperature)
	ag := s.newAgentFromConfig(agentConfig, target.Client, s.toolManagerForSession(sess))

	content, usage, err := ag.RunWithEvents(runCtx, sess, req.Message, func(ev agent.Event) {
		if event, ok := s.agentEventToStreamEvent(sess, target.ProviderType, ev); ok {
			_ = writeEvent(event)
		}
	})

	if err != nil {
		if errors.Is(err, agent.ErrTransitionPaused) {
			s.publishSessionCatalog("session_updated", sess)
			_ = writeEvent(ChatStreamEvent{Type: "provider_transition", Transition: session.CurrentProviderTransition(sess), Status: string(sess.Status), Error: err.Error()})
			return
		}
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
		s.triggerSerialSessionQueueIfAdvanceable(sess)
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

	// The LLM summary pass runs in the background so `done` and queue
	// advancement are not delayed by an extra provider round-trip.
	s.refreshSessionSummaryAsync(sess)
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
	s.triggerSerialSessionQueueIfAdvanceable(sess)
	s.publishSessionCatalog("session_updated", sess)
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
