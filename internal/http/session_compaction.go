package http

import (
	"net/http"

	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
)

func (s *Server) handleCompactSessionContext(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	ctx, release, err := s.sessionManager.AcquireRun(r.Context(), sessionID)
	if err != nil || s.activeSessionRunCount(sessionID) > 0 {
		if release != nil {
			release()
		}
		s.errorResponse(w, http.StatusConflict, "Wait for the active run to finish before compacting context")
		return
	}
	defer release()

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found")
		return
	}
	if sessionPromptEditing(sess) || sess.Status == session.StatusRunning || sess.Status == session.StatusWaitingExternal || sess.Status == session.StatusQueued {
		s.errorResponse(w, http.StatusConflict, "Session is not available for context compaction")
		return
	}
	if s.hasDirectAgentTarget(sess) {
		s.errorResponse(w, http.StatusConflict, "Manual context compaction is not supported for this agent")
		return
	}
	if transition := session.CurrentProviderTransition(sess); transition != nil && !transition.Terminal() {
		s.errorResponse(w, http.StatusConflict, "Resolve the pending provider transition first")
		return
	}

	providerType := s.resolveSessionProviderType(sess)
	model := s.resolveSessionModel(sess, providerType)
	lastUserPrompt := ""
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role == "user" {
			lastUserPrompt = sess.Messages[i].Content
			break
		}
	}
	// Reuse the session's sticky route without allowing this auxiliary call to change routing effort.
	target, err := s.resolveExecutionTargetBase(ctx, providerType, model, lastUserPrompt, sess)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Unable to resolve the session provider: "+err.Error())
		return
	}
	systemPrompt, err := s.buildSystemPromptForSession(sess)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	compactAgent := s.newAgentFromConfig(
		s.agentConfigFromTarget(sess, target, systemPrompt, s.config.MaxSteps, s.config.Temperature),
		target.Client,
		s.toolManagerForSession(sess),
	)
	if err := compactAgent.CompactContext(ctx, sess); err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to compact session context: "+err.Error())
		return
	}

	s.refreshSessionSummaryAsync(sess)
	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
}
