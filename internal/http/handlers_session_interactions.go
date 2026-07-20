package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
)

func (s *Server) handleGetPendingQuestion(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	question, err := s.sessionManager.GetPendingQuestion(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Failed to get question: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"question": question})
}

func (s *Server) handleGetTaskProgress(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	progress, err := s.sessionManager.GetSessionTaskProgress(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Failed to get task progress: "+err.Error())
		return
	}

	stats := parseTaskProgressStats(progress)

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"content":         progress,
		"total_tasks":     stats.Total,
		"completed_tasks": stats.Completed,
		"progress_pct":    stats.ProgressPct,
	})
}

func parseTaskProgressStats(content string) struct {
	Total       int
	Completed   int
	ProgressPct int
} {
	lines := strings.Split(content, "\n")
	total := 0
	completed := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[ ]") {
			total++
		} else if strings.HasPrefix(trimmed, "[x]") || strings.HasPrefix(trimmed, "[X]") {
			total++
			completed++
		}
	}

	pct := 0
	if total > 0 {
		pct = (completed * 100) / total
	}

	return struct {
		Total       int
		Completed   int
		ProgressPct int
	}{
		Total:       total,
		Completed:   completed,
		ProgressPct: pct,
	}
}

func (s *Server) handleAnswerQuestion(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	var req struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Answer == "" {
		s.errorResponse(w, http.StatusBadRequest, "answer is required")
		return
	}

	if err := s.sessionManager.AnswerQuestion(sessionID, req.Answer); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to answer question: "+err.Error())
		return
	}

	s.resumeSessionAfterQuestionAnswer(sessionID, req.Answer)
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

func (s *Server) handleInjectSessionMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	var req struct {
		Message string                `json:"message"`
		Images  []MessageImagePayload `json:"images,omitempty"`
	}
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

	sess, err := s.sessionManager.InjectUserMessage(sessionID, req.Message, images)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Failed to inject message: "+err.Error())
		return
	}
	defer s.queueTelegramSessionMessageSync(sess.ID)

	logging.LogSession("message_injected", sess.ID, "via HTTP")

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"session": s.sessionToResponse(sess),
	})
}

func (s *Server) resumeSessionAfterQuestionAnswer(sessionID string, userAnswer string) {
	runCtx, cancelRun := context.WithCancel(s.sessionRunParentContext())
	runID := s.registerActiveSessionRun(sessionID, cancelRun)

	go func() {
		defer func() {
			cancelRun()
			s.unregisterActiveSessionRun(sessionID, runID)
		}()

		sess, err := s.sessionManager.Get(sessionID)
		if err != nil {
			logging.Warn("Failed to reload session after answer: session=%s error=%v", sessionID, err)
			return
		}
		if sess.Status != session.StatusRunning {
			return
		}
		defer s.queueTelegramSessionMessageSync(sess.ID)

		providerType := s.resolveSessionProviderType(sess)
		model := s.resolveSessionModel(sess, providerType)
		routingPrompt := messageForRouting(userAnswer, 0)
		target, err := s.resolveExecutionTarget(runCtx, providerType, model, routingPrompt, sess)
		if err != nil {
			sess.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", err.Error()), nil)
			sess.SetStatus(session.StatusFailed)
			_ = s.sessionManager.Save(sess)
			logging.Warn("Provider resolution failed while resuming question answer: session=%s error=%v", sessionID, err)
			return
		}
		if setSessionRoutedProviderAndModel(sess, providerType, target.ProviderType, target.Model, target.RoutingRule, target.RoutingReason) {
			if err := s.sessionManager.Save(sess); err != nil {
				logging.Warn("Failed to persist routed metadata while resuming session: %v", err)
			}
		}
		if event := routerDecisionStreamEvent(providerType, target); event != nil {
			s.publishSessionEvent(sess.ID, *event)
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
			UseProviderSession:  target.ProviderSessions,
		}
		ag := s.newAgentFromConfig(agentConfig, target.Client, s.toolManagerForSession(sess))
		_, _, err = ag.RunWithEvents(runCtx, sess, userAnswer, func(ev agent.Event) {
			if ev.Type == agent.EventProviderTrace && ev.Provider != nil {
				s.applyProviderTraceToSession(sess, target.ProviderType, ev.Provider)
			}
		})
		if err != nil {
			if isCancellationError(err) {
				sess.SetStatus(session.StatusPaused)
				_ = s.sessionManager.Save(sess)
				return
			}
			adaptedErr := s.adaptProviderErrorMessage(target.ProviderType, err)
			addRequestFailedAssistantMessage(sess, adaptedErr)
			sess.SetStatus(session.StatusFailed)
			_ = s.sessionManager.Save(sess)
		}
		if fresh, freshErr := s.sessionManager.Get(sessionID); freshErr == nil && sessionIsSerialQueuedAutoRun(fresh) && serialQueueCanAdvanceAfterStatus(fresh.Status) {
			s.triggerSerialSessionQueueForSession(fresh)
		}
	}()
}
