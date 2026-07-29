// handlers_sessions.go keeps session lifecycle handlers grouped after splitting server.go by responsibility.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
)

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.sessionManager.List()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list sessions: "+err.Error())
		return
	}

	filterA2A := r.URL.Query().Get("a2a_inbound") == "true"
	includeMetadata := r.URL.Query().Get("include_metadata") == "true"
	filterProjectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	metadataKeys := parseSessionListMetadataKeys(r.URL.Query().Get("metadata_keys"))

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
			projectID = strings.TrimSpace(*sess.ProjectID)
		}
		if filterProjectID != "" && projectID != filterProjectID {
			continue
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
			Summary:            sess.Summary,
			Status:             string(sess.Status),
			TotalTokens:        inputTokens + outputTokens,
			InputTokens:        inputTokens,
			OutputTokens:       outputTokens,
			RunDurationSeconds: sessionRunDurationSeconds(sess.CreatedAt, sess.UpdatedAt, string(sess.Status)),
			TaskProgress:       sess.TaskProgress,
			PromptCache:        sessionPromptCache(sess),
			CreatedAt:          sess.CreatedAt,
			UpdatedAt:          sess.UpdatedAt,
			A2AInbound:         isInbound,
			A2ASourceAgentID:   sourceAgentID,
			A2ASourceAgentName: sourceAgentName,
		}
		if includeMetadata {
			item.Metadata = filterSessionListMetadata(sess.Metadata, metadataKeys)
		}
		items = append(items, item)
	}

	s.jsonResponse(w, http.StatusOK, items)
}

func parseSessionListMetadataKeys(raw string) map[string]struct{} {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	keys := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		key := strings.TrimSpace(part)
		if key == "" {
			continue
		}
		keys[key] = struct{}{}
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}

func filterSessionListMetadata(metadata map[string]interface{}, keys map[string]struct{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	if len(keys) == 0 {
		return metadata
	}
	filtered := make(map[string]interface{}, len(keys))
	for key := range keys {
		if value, ok := metadata[key]; ok {
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
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
	applyLeadingSessionQueueDirective(&req)
	req.ParentID = strings.TrimSpace(req.ParentID)
	linkType, err := normalizeSessionLinkType(req.LinkType)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	req.LinkType = linkType
	queueMode, err := normalizeSessionQueueMode(req.QueueMode)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	req.QueueMode = queueMode
	if queueMode == sessionQueueModeSerial {
		req.Queued = true
	}

	var parentSession *session.Session
	var linkedContinuation *linkedContinuationContext
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
		if req.LinkType == sessionLinkTypeContinuation {
			context := buildLinkedContinuationContext(parentSession, req.Task)
			req.Task = context.Prompt
			linkedContinuation = &context
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
	if linkedContinuation != nil {
		applyLinkedContinuationSessionMetadata(sess, *linkedContinuation)
	}
	if req.QueueMode != "" {
		if sess.Metadata == nil {
			sess.Metadata = make(map[string]interface{})
		}
		sess.Metadata[sessionQueueModeMetadataKey] = req.QueueMode
		if req.QueueMode == sessionQueueModeSerial {
			sess.Metadata[sessionQueueAutoStartKey] = true
		}
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist session queue metadata: %v", err)
		}
	}

	if req.Task != "" || len(images) > 0 {
		if linkedContinuation != nil {
			sess.AddUserMessageWithImagesAndMetadata(req.Task, images, linkedContinuationMessageMetadata(*linkedContinuation))
		} else {
			sess.AddUserMessageWithImages(req.Task, images)
		}
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Error("Failed to save session with initial task: %v", err)
		}
	}

	directUnifiedAgentID := strings.TrimSpace(req.UnifiedAgentID)
	directDockerAgentID := strings.TrimSpace(req.DockerAgentID)
	if directUnifiedAgentID != "" || directDockerAgentID != "" {
		if directUnifiedAgentID != "" {
			projectID := strings.TrimSpace(req.ProjectID)
			if _, _, err := s.definitionForUnifiedAgent(directUnifiedAgentID, projectID); err != nil {
				s.errorResponse(w, http.StatusBadRequest, "Unified agent not found: "+directUnifiedAgentID)
				return
			}
			sess.Metadata["unified_agent_id"] = directUnifiedAgentID
		}
		if directDockerAgentID != "" {
			sess.Metadata["docker_agent_id"] = directDockerAgentID
		}
		sess.Metadata["launch_target"] = "agent"
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
		providerType = s.defaultSessionProviderRef()
	}
	model := s.resolveCreateSessionModel(providerType, req.Model)
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
	if err := s.attachTeamRunToCreatedSession(sess, strings.TrimSpace(req.TeamID)); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.ensureSessionSystemPromptSnapshot(sess)
	if req.Queued && sessionIsSerialQueuedAutoRun(sess) {
		s.triggerSerialSessionQueueForSession(sess)
	}
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

	if sessionIsQueuePaused(sess) {
		setSessionQueuePaused(sess, false)
		if err := s.sessionManager.Save(sess); err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to start session: "+err.Error())
			return
		}
	}

	if sessionIsSerialQueuedAutoRun(sess) {
		if sess.Metadata == nil {
			sess.Metadata = map[string]interface{}{}
		}
		delete(sess.Metadata, sessionQueueAutoStartKey)
		delete(sess.Metadata, sessionQueueModeMetadataKey)
		if err := s.sessionManager.Save(sess); err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to start session: "+err.Error())
			return
		}
		logging.LogSession("started", sess.ID, fmt.Sprintf("agent=%s manual override removed serial queue auto-start", sess.AgentID))
		s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
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

func (s *Server) handleUpdateSessionNeedsFeedback(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	var req UpdateSessionNeedsFeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	setSessionNeedsFeedback(sess, req.NeedsFeedback)
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update session needs_feedback: "+err.Error())
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
		model := normalizeModelForProvider(providerType, *req.Model)
		if model == "" {
			delete(sess.Metadata, "model")
		} else {
			sess.Metadata["model"] = model
		}
	}

	delete(sess.Metadata, "routed_provider")
	delete(sess.Metadata, "routed_model")

	// A previously recorded active fallback node is stale once the session no longer targets a fallback provider.
	if !config.IsFallbackAggregateRef(provider) && providerType != config.ProviderFallback {
		delete(sess.Metadata, fallbackActiveProviderMetadataKey)
		delete(sess.Metadata, fallbackActiveModelMetadataKey)
	}

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
	metadataKeys := parseSessionListMetadataKeys(r.URL.Query().Get("metadata_keys"))

	var sess *session.Session
	var err error
	if includeMessages || includeMetadata {
		sess, err = s.sessionManager.Get(sessionID)
	} else {
		sess, err = s.sessionManager.GetSummary(sessionID)
	}
	if err != nil {
		if resp, ok, proxyErr := s.getDockerDelegatedSession(r.Context(), sessionID, includeMessages, includeMetadata); ok {
			s.jsonResponse(w, http.StatusOK, resp)
			return
		} else if proxyErr != nil {
			logging.Debug("Docker session proxy lookup failed for %s: %v", sessionID, proxyErr)
		}
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
	} else {
		resp.Metadata = filterSessionListMetadata(resp.Metadata, metadataKeys)
	}
	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleDownloadSessionLog(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(chi.URLParam(r, "sessionID"))
	if sessionID == "" {
		s.errorResponse(w, http.StatusBadRequest, "Session ID is required")
		return
	}

	logPath := s.sessionManager.JSONLPath(sessionID)
	if logPath == "" {
		s.errorResponse(w, http.StatusNotFound, "Session log is not configured")
		return
	}
	if _, err := os.Stat(logPath); err != nil {
		if os.IsNotExist(err) {
			s.errorResponse(w, http.StatusNotFound, "Session log not found")
			return
		}
		s.errorResponse(w, http.StatusInternalServerError, "Failed to access session log: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="session-%s.jsonl"`, filepath.Base(sessionID)))
	http.ServeFile(w, r, logPath)
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
