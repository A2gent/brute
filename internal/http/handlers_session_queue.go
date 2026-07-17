package http

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

type QueueSessionsRequest struct {
	SessionIDs []string `json:"session_ids"`
}

type QueueSessionsMutationResponse struct {
	Updated []string `json:"updated"`
	Skipped []string `json:"skipped"`
}

func (s *Server) handlePauseQueuedSessions(w http.ResponseWriter, r *http.Request) {
	s.mutateQueuedSessions(w, r, true)
}

func (s *Server) handleResumeQueuedSessions(w http.ResponseWriter, r *http.Request) {
	s.mutateQueuedSessions(w, r, false)
}

func (s *Server) mutateQueuedSessions(w http.ResponseWriter, r *http.Request, pause bool) {
	var req QueueSessionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	ids := make([]string, 0, len(req.SessionIDs))
	seen := make(map[string]struct{}, len(req.SessionIDs))
	for _, rawID := range req.SessionIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "session_ids is required")
		return
	}

	resp := QueueSessionsMutationResponse{
		Updated: make([]string, 0, len(ids)),
		Skipped: make([]string, 0),
	}
	scopesToResume := make(map[string]struct{})

	for _, id := range ids {
		sess, err := s.sessionManager.Get(id)
		if err != nil {
			resp.Skipped = append(resp.Skipped, id)
			continue
		}
		if sess.Status != session.StatusQueued {
			resp.Skipped = append(resp.Skipped, id)
			continue
		}
		if pause {
			if sessionIsQueuePaused(sess) {
				resp.Skipped = append(resp.Skipped, id)
				continue
			}
			setSessionQueuePaused(sess, true)
		} else {
			if !sessionIsQueuePaused(sess) {
				resp.Skipped = append(resp.Skipped, id)
				continue
			}
			setSessionQueuePaused(sess, false)
			if sessionIsSerialQueuedAutoRun(sess) {
				scopesToResume[serialQueueScopeForSession(sess)] = struct{}{}
			}
		}
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to update queue pause state for session %s: %v", id, err)
			resp.Skipped = append(resp.Skipped, id)
			continue
		}
		resp.Updated = append(resp.Updated, id)
	}

	if !pause {
		for scope := range scopesToResume {
			s.triggerSerialSessionQueue(scope)
		}
	}

	s.jsonResponse(w, http.StatusOK, resp)
}
