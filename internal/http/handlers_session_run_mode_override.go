package http

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
)

type sessionRunModeOverrideRequest struct {
	QueueMode string `json:"queue_mode"`
}

// handleOverrideSessionRunMode changes an automatic decision without discarding
// the original Jev evidence. Active runs are protected by the same lease used by
// message execution, so a running session cannot be moved out from under tools.
func (s *Server) handleOverrideSessionRunMode(w http.ResponseWriter, r *http.Request) {
	var req sessionRunModeOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	mode := strings.TrimSpace(req.QueueMode)
	if mode != "now" && mode != sessionQueueModeSerial {
		s.errorResponse(w, http.StatusBadRequest, "queue_mode must be now or serial")
		return
	}

	id := chi.URLParam(r, "sessionID")
	_, release, err := s.sessionManager.AcquireRun(r.Context(), id)
	if err != nil {
		s.errorResponse(w, http.StatusConflict, "Session is busy")
		return
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	sess, err := s.sessionManager.Get(id)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found")
		return
	}
	if sessionPromptEditing(sess) {
		s.errorResponse(w, http.StatusConflict, "Session prompt is being edited")
		return
	}
	if decision := sess.RunModeDecision(); decision == nil || decision.Source != "jev" {
		s.errorResponse(w, http.StatusConflict, "Session has no automatic run-mode decision")
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}

	switch mode {
	case "now":
		if sess.Status != session.StatusQueued {
			s.errorResponse(w, http.StatusConflict, "Only a queued session can be started now")
			return
		}
		delete(sess.Metadata, sessionQueueModeMetadataKey)
		delete(sess.Metadata, sessionQueueAutoStartKey)
		delete(sess.Metadata, sessionQueuePausedKey)
		sess.Metadata["run_mode_override"] = "now"
		sess.SetStatus(session.StatusRunning)
	case sessionQueueModeSerial:
		if sess.Status != session.StatusQueued && sess.Status != session.StatusRunning && sess.Status != session.StatusPaused {
			s.errorResponse(w, http.StatusConflict, "Only a queued or idle running session can be moved to the serial queue")
			return
		}
		sess.Metadata[sessionQueueModeMetadataKey] = sessionQueueModeSerial
		sess.Metadata[sessionQueueAutoStartKey] = true
		sess.Metadata["run_mode_override"] = sessionQueueModeSerial
		sess.SetStatus(session.StatusQueued)
	}

	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist session run-mode override: %v", err)
		s.errorResponse(w, http.StatusInternalServerError, "Failed to change session run mode")
		return
	}

	release()
	released = true
	if mode == sessionQueueModeSerial {
		s.triggerSerialSessionQueueForSession(sess)
	}
	s.jsonResponse(w, http.StatusOK, s.sessionToResponse(sess))
}
