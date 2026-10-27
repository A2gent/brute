// handlers_sessions.go keeps session lifecycle handlers grouped after splitting server.go by responsibility.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	var sessions []*session.Session
	var err error
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if relatedTo := strings.TrimSpace(r.URL.Query().Get("related_to")); relatedTo != "" {
		sessions, err = s.sessionManager.ListRelatedForDisplay(projectID, relatedTo)
	} else {
		sessions, err = s.sessionManager.ListForDisplay(projectID)
	}
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list sessions: "+err.Error())
		return
	}

	filterA2A := r.URL.Query().Get("a2a_inbound") == "true"
	includeMetadata := r.URL.Query().Get("include_metadata") == "true"
	filterProjectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	searchQuery := strings.TrimSpace(r.URL.Query().Get("search"))
	metadataKeys := parseSessionListMetadataKeys(r.URL.Query().Get("metadata_keys"))
	// Archived (completed) sessions are hidden unless explicitly requested.
	archiveFilter := normalizeSessionArchiveFilter(r.URL.Query().Get("archived"))

	var since time.Time
	var recentChanges map[string]sessionCatalogChange
	if raw := strings.TrimSpace(r.URL.Query().Get("updated_since")); raw != "" {
		since, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Invalid updated_since: expected RFC3339 timestamp")
			return
		}
		recentChanges = s.catalogHub().changesSince(since)
	}

	var dialogueMatches map[string]struct{}
	if searchQuery != "" {
		dialogueMatches, err = s.sessionManager.SearchDialogueSessionIDs(searchQuery, filterProjectID)
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to search sessions: "+err.Error())
			return
		}
	}

	// Worktrees come from one query and project settings from one lookup per project,
	// so the list does not query per row; catalog events load the same fields per session.
	worktrees, err := s.store.ListSessionWorktrees()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list session worktrees: "+err.Error())
		return
	}
	parents := make(map[string]string, len(sessions))
	for _, sess := range sessions {
		if sess.ParentID != nil {
			parents[sess.ID] = *sess.ParentID
		}
	}
	projectSettings := make(map[string]map[string]string)

	items := make([]SessionListItem, 0, len(sessions))
	for _, sess := range sessions {
		if !sessionMatchesArchiveFilter(sess, archiveFilter) {
			continue
		}
		if dialogueMatches != nil {
			if _, matched := dialogueMatches[sess.ID]; !matched {
				continue
			}
		}
		item := s.sessionListItem(sess)
		if filterA2A && !item.A2AInbound {
			continue
		}
		if filterProjectID != "" && item.ProjectID != filterProjectID {
			continue
		}
		if !since.IsZero() && sess.UpdatedAt.Before(since) {
			if _, changed := recentChanges[sess.ID]; !changed {
				continue
			}
		}
		if includeMetadata {
			item.Metadata = filterSessionListMetadata(sess.Metadata, metadataKeys)
		}
		if item.ProjectID != "" {
			settings, loaded := projectSettings[item.ProjectID]
			if !loaded {
				if project, err := s.store.GetProject(item.ProjectID); err == nil {
					settings = normalizeProjectSettings(project.Settings)
				}
				projectSettings[item.ProjectID] = settings
			}
			// Continuations work in their ancestor's worktree, same walk as sessionWorktree.
			wt := worktrees[sess.ID]
			for id := sess.ID; wt == nil && parents[id] != ""; id = parents[id] {
				wt = worktrees[parents[id]]
			}
			item.Worktree, item.MainFolder = sessionWorktreePayload(wt, settings)
		}
		items = append(items, item)
	}

	if !since.IsZero() {
		// Tombstones let a resyncing client drop sessions deleted while it was away.
		for id, change := range recentChanges {
			if change.deleted && (filterProjectID == "" || change.projectID == filterProjectID) {
				items = append(items, SessionListItem{ID: id, ProjectID: change.projectID, Deleted: true, UpdatedAt: change.at})
			}
		}
	}

	s.jsonResponse(w, http.StatusOK, items)
}

// sessionListItem builds the /sessions row; catalog events reuse it so a
// streamed row is identical to a listed one.
func (s *Server) sessionListItem(sess *session.Session) SessionListItem {
	isInbound, sourceAgentID, sourceAgentName := sessionA2AMeta(sess)
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
	jobID := ""
	if sess.JobID != nil {
		jobID = *sess.JobID
	}
	inputTokens, outputTokens := sessionInputOutputTokens(sess)
	channel, channelDetail := sessionChannel(sess)
	return SessionListItem{
		RunModeDecision:    sess.RunModeDecision(),
		ID:                 sess.ID,
		AgentID:            sess.AgentID,
		ParentID:           parentID,
		JobID:              jobID,
		Channel:            channel,
		ChannelDetail:      channelDetail,
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
		PromptCache:        sessionPromptCache(sess, s.config),
		CreatedAt:          sess.CreatedAt,
		UpdatedAt:          sess.UpdatedAt,
		ArchivedAt:         sessionArchivedAt(sess),
		A2AInbound:         isInbound,
		A2ASourceAgentID:   sourceAgentID,
		A2ASourceAgentName: sourceAgentName,
	}
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
	if req.ParentID != "" {
		s.sessionTreeMu.RLock()
		defer s.sessionTreeMu.RUnlock()
	}
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
		if sessionArchivedAt(parentSession) != nil {
			s.errorResponse(w, http.StatusConflict, "Parent session is archived; restore it before creating a child")
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
	if linkedContinuation != nil {
		images = linkedContinuationImages(parentSession, images)
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.TaskID = strings.TrimSpace(req.TaskID)
	var linkedTask *storage.Task
	if req.TaskID != "" {
		if req.ProjectID == "" {
			s.errorResponse(w, http.StatusBadRequest, "project_id is required when task_id is set")
			return
		}
		linkedTask, err = s.store.GetTask(req.ProjectID, req.TaskID)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Task not found: "+err.Error())
			return
		}
		if linkedTask.SessionID != "" {
			s.errorResponse(w, http.StatusConflict, "Task already has a linked session")
			return
		}
	}
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
	s.applySessionRunMode(r.Context(), &req, sess)
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
			_, discoveredProjectID, err := s.definitionForUnifiedAgent(directUnifiedAgentID, projectID)
			if err != nil {
				s.errorResponse(w, http.StatusBadRequest, "Unified agent not found: "+directUnifiedAgentID)
				return
			}
			// WHY: mirror sub-agent launch - bind the session to the agent's project when
			// the caller omitted project_id, but never allow cross-project launches.
			if projectID == "" && strings.TrimSpace(discoveredProjectID) != "" {
				if _, projectErr := s.store.GetProject(discoveredProjectID); projectErr != nil {
					s.errorResponse(w, http.StatusBadRequest, "Unified agent project not found: "+projectErr.Error())
					return
				}
				req.ProjectID = discoveredProjectID
				projectID = discoveredProjectID
			}
			if !agentVisibleInProjectSession(discoveredProjectID, projectID) {
				s.errorResponse(w, http.StatusBadRequest, "Unified agent belongs to another project: "+directUnifiedAgentID)
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
		if !agentVisibleInProjectSession(subAgentProjectID(sa), strings.TrimSpace(req.ProjectID)) {
			s.errorResponse(w, http.StatusBadRequest, "Sub-agent belongs to another project: "+sa.ID)
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
		// Child sessions reuse the worktree of their parent (see sessionWorktree).
		// Queued serial sessions get theirs when the queue starts them (runSerialQueuedSession).
		if req.ParentID == "" && !(req.Queued && sessionIsSerialQueuedAutoRun(sess)) {
			if err := s.createSessionWorktree(sess.ID, req.ProjectID, strings.TrimSpace(req.Branch), strings.TrimSpace(req.BaseRef)); err != nil {
				_ = s.sessionManager.Delete(sess.ID)
				s.errorResponse(w, http.StatusBadRequest, "Failed to create session worktree: "+err.Error())
				return
			}
		} else if req.ParentID == "" && strings.TrimSpace(req.Branch+req.BaseRef) != "" {
			// The deferred worktree is created at start time; keep the requested branch and base until then.
			sess.Metadata["worktree_branch"] = strings.TrimSpace(req.Branch)
			sess.Metadata["worktree_base_ref"] = strings.TrimSpace(req.BaseRef)
			if err := s.sessionManager.Save(sess); err != nil {
				logging.Warn("Failed to persist session worktree branch: %v", err)
			}
		}
	}
	if linkedTask != nil {
		if _, err := s.store.UpdateTask(req.ProjectID, linkedTask.ID, storage.TaskUpdate{SessionID: &sess.ID}); err != nil {
			_ = s.sessionManager.Delete(sess.ID)
			s.errorResponse(w, http.StatusInternalServerError, "Failed to link task session: "+err.Error())
			return
		}
		sess.Metadata["task_id"] = linkedTask.ID
		sess.Metadata["task_ref"] = linkedTask.Ref
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist task session metadata: %v", err)
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
		RunModeDecision: sess.RunModeDecision(),
		ID:              sess.ID,
		AgentID:         sess.AgentID,
		ParentID:        req.ParentID,
		LinkType:        req.LinkType,
		ProjectID:       projectID,
		Provider:        providerType,
		Model:           model,
		ReasoningEffort: sessionReasoningEffort(sess),
		Status:          string(sess.Status),
		CreatedAt:       sess.CreatedAt,
	})
}

func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	_, release, err := s.sessionManager.AcquireRun(r.Context(), sessionID)
	if err != nil {
		s.errorResponse(w, 409, "Session is busy")
		return
	}
	defer release()

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	if sessionPromptEditing(sess) {
		s.errorResponse(w, 409, "Session prompt is being edited")
		return
	}

	if sess.Status != session.StatusQueued {
		s.errorResponse(w, http.StatusBadRequest, "Session is not in queued status")
		return
	}

	if sessionIsQueuePaused(sess) {
		setSessionQueuePaused(sess, false)
	}

	if sessionIsSerialQueuedAutoRun(sess) {
		// Serial-queued sessions defer worktree creation until execution; manual Start
		// bypasses the queue, so provision the isolated workspace before marking running.
		if sess.ProjectID != nil && (sess.ParentID == nil || *sess.ParentID == "") && s.sessionWorktree(sess) == nil {
			branch, _ := sess.Metadata["worktree_branch"].(string)
			baseRef, _ := sess.Metadata["worktree_base_ref"].(string)
			if err := s.createSessionWorktree(sess.ID, *sess.ProjectID, branch, baseRef); err != nil {
				s.errorResponse(w, http.StatusInternalServerError, "Failed to start session: "+err.Error())
				return
			}
		}
		if sess.Metadata == nil {
			sess.Metadata = map[string]interface{}{}
		}
		delete(sess.Metadata, sessionQueueAutoStartKey)
		delete(sess.Metadata, sessionQueueModeMetadataKey)
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
	s.handleUpdateSessionProviderTransition(w, r)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	includeMessages := r.URL.Query().Get("include_messages") != "false"
	includeMetadata := r.URL.Query().Get("include_metadata") != "false"
	metadataKeys := parseSessionListMetadataKeys(r.URL.Query().Get("metadata_keys"))

	messageLimit := 0
	if raw := r.URL.Query().Get("message_limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 500 {
			s.errorResponse(w, http.StatusBadRequest, "message_limit must be between 1 and 500")
			return
		}
		messageLimit = parsed
	}
	var hasMore bool
	var sess *session.Session
	var err error
	if includeMessages && messageLimit > 0 {
		sess, hasMore, err = s.sessionManager.GetPageForDisplay(sessionID, messageLimit, r.URL.Query().Get("before_message"))
	} else if includeMessages || includeMetadata {
		sess, err = s.sessionManager.GetForDisplay(sessionID, includeMessages)
	} else {
		sess, err = s.sessionManager.GetSummary(sessionID)
	}
	if errors.Is(err, storage.ErrMessageCursorMissing) {
		s.errorResponse(w, http.StatusConflict, "Session history changed; reload the latest page")
		return
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
		if _, err := s.ensureDisplaySystemPromptSnapshot(sess); err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to load system prompt snapshot: "+err.Error())
			return
		}
	}

	resp := s.sessionToResponse(sess)
	if includeMessages {
		resp.Messages = s.hydrateDirectAgentActivity(r.Context(), sess, resp.Messages)
	}
	if includeMessages && messageLimit > 0 {
		resp.MessagePage = &MessagePagePayload{HasMore: hasMore}
		if len(sess.Messages) > 0 {
			resp.MessagePage.BeforeMessage = sess.Messages[0].ID
		}
	}
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
		// WHY: descendants that are still running must outlive their deleted ancestors;
		// they are detached (becoming roots) so the storage-level cascade does not reach them.
		var detach []*session.Session
		sessionIDsToDelete, detach = sessionDeleteCascade(allSessions, sessionID, s.sessionIsBusy)
		for _, survivor := range detach {
			survivor.ParentID = nil
			if saveErr := s.sessionManager.Save(survivor); saveErr != nil {
				s.errorResponse(w, http.StatusInternalServerError, "Failed to detach running child session: "+saveErr.Error())
				return
			}
		}
	}

	for _, id := range sessionIDsToDelete {
		s.cancelActiveSessionRuns(id)
		s.closeBrowserPageForSession(id)
	}
	for _, id := range sessionIDsToDelete {
		if err := s.teardownSessionWorktree(id, completeModeDiscard); err != nil {
			s.errorResponse(w, http.StatusConflict, err.Error())
			return
		}
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
	s.forgetSessionSummaries(sessionIDsToDelete)

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
