// handlers_sessions.go keeps session lifecycle handlers grouped after splitting server.go by responsibility.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.sessionManager.List()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list sessions: "+err.Error())
		return
	}

	filterA2A := r.URL.Query().Get("a2a_inbound") == "true"
	includeMetadata := r.URL.Query().Get("include_metadata") == "true"

	items := make([]SessionListItem, 0, len(sessions))
	for _, sess := range sessions {
		isInbound, sourceAgentID, sourceAgentName := sessionA2AMeta(sess)
		if filterA2A && !isInbound {
			continue
		}
		parentID := ""
		if sess.ParentID != nil {
			parentID = *sess.ParentID
		}
		provider, model := sessionProviderAndModel(sess)
		routedProvider, routedModel := sessionRoutedProviderAndModel(sess)
		projectID := ""
		if sess.ProjectID != nil {
			projectID = *sess.ProjectID
		}
		jobID := ""
		if sess.JobID != nil {
			jobID = *sess.JobID
		}
		inputTokens, outputTokens := sessionInputOutputTokens(sess)
		item := SessionListItem{
			ID:                 sess.ID,
			AgentID:            sess.AgentID,
			ParentID:           parentID,
			JobID:              jobID,
			LinkType:           sessionLinkType(sess),
			ProjectID:          projectID,
			Provider:           provider,
			Model:              model,
			RoutedProvider:     routedProvider,
			RoutedModel:        routedModel,
			Title:              sess.Title,
			Status:             string(sess.Status),
			TotalTokens:        inputTokens + outputTokens,
			InputTokens:        inputTokens,
			OutputTokens:       outputTokens,
			RunDurationSeconds: sessionRunDurationSeconds(sess.CreatedAt, sess.UpdatedAt, string(sess.Status)),
			TaskProgress:       sess.TaskProgress,
			CreatedAt:          sess.CreatedAt,
			UpdatedAt:          sess.UpdatedAt,
			A2AInbound:         isInbound,
			A2ASourceAgentID:   sourceAgentID,
			A2ASourceAgentName: sourceAgentName,
		}
		if includeMetadata {
			item.Metadata = sess.Metadata
		}
		items = append(items, item)
	}

	s.jsonResponse(w, http.StatusOK, items)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.AgentID == "" {
		req.AgentID = "build"
	}
	req.ParentID = strings.TrimSpace(req.ParentID)
	linkType, err := normalizeSessionLinkType(req.LinkType)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	req.LinkType = linkType

	var parentSession *session.Session
	if req.ParentID != "" {
		parentSession, err = s.sessionManager.Get(req.ParentID)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Parent session not found: "+err.Error())
			return
		}
		if req.ProjectID == "" && parentSession.ProjectID != nil {
			req.ProjectID = strings.TrimSpace(*parentSession.ProjectID)
		}
		if req.LinkType == "" {
			req.LinkType = sessionLinkTypeContinuation
		}
	}
	images, imagesErr := normalizeIncomingImages(req.Images)
	if imagesErr != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid images payload: "+imagesErr.Error())
		return
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID != "" {
		if _, err := s.store.GetProject(req.ProjectID); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Project not found: "+err.Error())
			return
		}
	}

	// Create session based on queued flag
	var sess *session.Session
	if req.ParentID != "" {
		sess, err = s.sessionManager.CreateWithParent(req.AgentID, req.ParentID)
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to create session: "+err.Error())
			return
		}
		if req.Queued {
			sess.SetStatus(session.StatusQueued)
			if err := s.sessionManager.Save(sess); err != nil {
				s.errorResponse(w, http.StatusInternalServerError, "Failed to create session: "+err.Error())
				return
			}
		}
	} else if req.Queued {
		sess, err = s.sessionManager.CreateQueued(req.AgentID)
	} else {
		sess, err = s.sessionManager.Create(req.AgentID)
	}
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create session: "+err.Error())
		return
	}
	if req.Metadata != nil {
		if sess.Metadata == nil {
			sess.Metadata = make(map[string]interface{})
		}
		for key, value := range req.Metadata {
			k := strings.TrimSpace(key)
			if k == "" {
				continue
			}
			sess.Metadata[k] = value
		}
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist session metadata: %v", err)
		}
	}

	if req.Task != "" || len(images) > 0 {
		sess.AddUserMessageWithImages(req.Task, images)

		if req.Task != "" && len(images) == 0 && len(req.Task) < 600 {
			settings, err := s.store.GetSettings()
			if err == nil {
				repeatEnabled := strings.TrimSpace(settings[repeatInitialPromptSettingKey])
				if repeatEnabled != "false" {

					sess.AddUserMessage(req.Task)
				}
			}
		}
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Error("Failed to save session with initial task: %v", err)
		}
	}

	subAgentID := strings.TrimSpace(req.SubAgentID)
	if subAgentID != "" {
		sa, saErr := s.store.GetSubAgent(subAgentID)
		if saErr != nil {
			s.errorResponse(w, http.StatusBadRequest, "Sub-agent not found: "+saErr.Error())
			return
		}
		sess.Metadata["sub_agent_id"] = sa.ID
		sess.Metadata["sub_agent_name"] = sa.Name
		if sa.Provider != "" {
			req.Provider = sa.Provider
		}
		if sa.Model != "" && req.Model == "" {
			req.Model = sa.Model
		}
		if req.ProjectID == "" && sa.ProjectID != nil {
			boundProjectID := strings.TrimSpace(*sa.ProjectID)
			if boundProjectID != "" {
				if _, projectErr := s.store.GetProject(boundProjectID); projectErr != nil {
					s.errorResponse(w, http.StatusBadRequest, "Sub-agent project not found: "+projectErr.Error())
					return
				}
				req.ProjectID = boundProjectID
			}
		}
	}

	providerType := config.NormalizeProviderRef(req.Provider)
	if providerType == "" {
		autoCfg := s.config.Providers[string(config.ProviderAutoRouter)]
		if s.autoRouterConfigured(autoCfg) {
			providerType = string(config.ProviderAutoRouter)
		} else {
			providerType = config.NormalizeProviderRef(s.config.ActiveProvider)
		}
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = s.resolveModelForProvider(config.ProviderType(providerType))
	}
	if req.LinkType != "" {
		sess.Metadata["link_type"] = req.LinkType
	}
	sess.Metadata["provider"] = providerType
	sess.Metadata["model"] = model
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist session provider metadata: %v", err)
	}
	if req.ProjectID != "" {
		sess.ProjectID = &req.ProjectID
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist session project metadata: %v", err)
		}
	}
	_ = s.ensureSessionSystemPromptSnapshot(sess)
	go func(sessionID string, task string) {
		syncCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		s.syncHTTPCreatedSessionToTelegram(syncCtx, sessionID, task)
	}(sess.ID, req.Task)

	logging.LogSession("created", sess.ID, fmt.Sprintf("agent=%s via HTTP", req.AgentID))

	projectID := ""
	if sess.ProjectID != nil {
		projectID = *sess.ProjectID
	}

	s.jsonResponse(w, http.StatusCreated, CreateSessionResponse{
		ID:        sess.ID,
		AgentID:   sess.AgentID,
		ParentID:  req.ParentID,
		LinkType:  req.LinkType,
		ProjectID: projectID,
		Provider:  providerType,
		Model:     model,
		Status:    string(sess.Status),
		CreatedAt: sess.CreatedAt,
	})
}

func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	if sess.Status != session.StatusQueued {
		s.errorResponse(w, http.StatusBadRequest, "Session is not in queued status")
		return
	}

	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to start session: "+err.Error())
		return
	}

	logging.LogSession("started", sess.ID, fmt.Sprintf("agent=%s via HTTP", sess.AgentID))

	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
}

func (s *Server) handleUpdateSessionProject(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	var req UpdateSessionProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.ProjectID == nil || strings.TrimSpace(*req.ProjectID) == "" {
		sess.ProjectID = nil
	} else {
		projectID := strings.TrimSpace(*req.ProjectID)
		if _, err := s.store.GetProject(projectID); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Project not found: "+err.Error())
			return
		}
		sess.ProjectID = &projectID
	}

	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update session project: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
}

func (s *Server) handleUpdateSessionProvider(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	var req UpdateSessionProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		s.errorResponse(w, http.StatusBadRequest, "Provider is required")
		return
	}

	providerType := config.ProviderType(provider)
	if !s.config.IsValidProvider(providerType) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid provider: "+provider)
		return
	}

	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}

	sess.Metadata["provider"] = provider

	if req.Model != nil {
		model := strings.TrimSpace(*req.Model)
		if model == "" {
			delete(sess.Metadata, "model")
		} else {
			sess.Metadata["model"] = model
		}
	}

	delete(sess.Metadata, "routed_provider")
	delete(sess.Metadata, "routed_model")

	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update session provider: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	includeMessages := r.URL.Query().Get("include_messages") != "false"
	includeMetadata := r.URL.Query().Get("include_metadata") != "false"

	var sess *session.Session
	var err error
	if includeMessages || includeMetadata {
		sess, err = s.sessionManager.Get(sessionID)
	} else {
		sess, err = s.sessionManager.GetSummary(sessionID)
	}
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	if includeMessages {
		_ = s.ensureSessionSystemPromptSnapshot(sess)
	}
	resp := s.sessionToResponse(sess)
	if !includeMessages {
		resp.Messages = nil
		resp.SystemPromptSnapshot = nil
	}
	if !includeMetadata {
		resp.Metadata = nil
	}
	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	sessionIDsToDelete := []string{sessionID}
	allSessions, listErr := s.sessionManager.List()
	if listErr != nil {
		logging.Warn("Failed to list sessions before delete cascade for %s: %v", sessionID, listErr)
	} else {
		childrenByParent := make(map[string][]string)
		for _, item := range allSessions {
			if item.ParentID == nil {
				continue
			}
			parentID := strings.TrimSpace(*item.ParentID)
			if parentID == "" {
				continue
			}
			childrenByParent[parentID] = append(childrenByParent[parentID], item.ID)
		}

		seen := map[string]struct{}{sessionID: {}}
		queue := []string{sessionID}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			for _, childID := range childrenByParent[current] {
				if _, exists := seen[childID]; exists {
					continue
				}
				seen[childID] = struct{}{}
				sessionIDsToDelete = append(sessionIDsToDelete, childID)
				queue = append(queue, childID)
			}
		}
	}

	for _, id := range sessionIDsToDelete {
		s.cancelActiveSessionRuns(id)
	}

	sess, err := s.sessionManager.Get(sessionID)
	if err == nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cleanupCancel()
		if cleanupErr := s.deleteTelegramTopicForSession(cleanupCtx, sess); cleanupErr != nil {
			logging.Warn("Telegram topic cleanup failed for session %s: %s", sessionID, sanitizeTelegramError(cleanupErr))
		}
	}

	for _, childSessionID := range sessionIDsToDelete[1:] {
		childSess, getErr := s.sessionManager.Get(childSessionID)
		if getErr != nil {
			continue
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(r.Context(), 20*time.Second)
		if cleanupErr := s.deleteTelegramTopicForSession(cleanupCtx, childSess); cleanupErr != nil {
			logging.Warn("Telegram topic cleanup failed for session %s: %s", childSessionID, sanitizeTelegramError(cleanupErr))
		}
		cleanupCancel()
	}

	if err := s.sessionManager.Delete(sessionID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete session: "+err.Error())
		return
	}

	logging.LogSession("deleted", sessionID, "via HTTP")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCancelSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	cancelledRuns := s.cancelActiveSessionRuns(sessionID)
	if cancelledRuns > 0 || strings.EqualFold(string(sess.Status), string(session.StatusRunning)) {
		sess.SetStatus(session.StatusPaused)
		if saveErr := s.sessionManager.Save(sess); saveErr != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to update session status: "+saveErr.Error())
			return
		}
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"session_id":      sessionID,
		"cancelled_runs":  cancelledRuns,
		"session_status":  string(sess.Status),
		"session_updated": sess.UpdatedAt,
	})
}

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
		if setSessionRoutedProviderAndModel(sess, providerType, target.ProviderType, target.Model) {
			if err := s.sessionManager.Save(sess); err != nil {
				logging.Warn("Failed to persist routed metadata while resuming session: %v", err)
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
			sess.AddAssistantMessage(fmt.Sprintf("Request failed: %s", adaptedErr.Error()), nil)
			sess.SetStatus(session.StatusFailed)
			_ = s.sessionManager.Save(sess)
		}
	}()
}

func (s *Server) registerActiveSessionRun(sessionID string, cancel context.CancelFunc) string {
	runID := uuid.New().String()
	s.activeRunsMu.Lock()
	defer s.activeRunsMu.Unlock()

	runs, ok := s.activeRuns[sessionID]
	if !ok {
		runs = make(map[string]context.CancelFunc)
		s.activeRuns[sessionID] = runs
	}
	runs[runID] = cancel
	return runID
}

func (s *Server) unregisterActiveSessionRun(sessionID, runID string) {
	s.activeRunsMu.Lock()
	defer s.activeRunsMu.Unlock()

	runs, ok := s.activeRuns[sessionID]
	if !ok {
		return
	}
	delete(runs, runID)
	if len(runs) == 0 {
		delete(s.activeRuns, sessionID)
	}
}

func (s *Server) cancelActiveSessionRuns(sessionID string) int {
	s.activeRunsMu.Lock()
	runs, ok := s.activeRuns[sessionID]
	if !ok || len(runs) == 0 {
		s.activeRunsMu.Unlock()
		return 0
	}
	cancels := make([]context.CancelFunc, 0, len(runs))
	for _, cancel := range runs {
		cancels = append(cancels, cancel)
	}
	delete(s.activeRuns, sessionID)
	s.activeRunsMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

func isCancellationError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "context canceled") || strings.Contains(lower, "cancelled")
}
