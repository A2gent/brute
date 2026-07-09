package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
)

func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "Streaming is not supported by the server")
		return
	}

	events, unsubscribe := s.SubscribeSessionEvents(sessionID)
	defer unsubscribe()

	writeSSE := func(event ChatStreamEvent) bool {
		payload, err := json.Marshal(event)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	writeHeartbeat := func() bool {
		if _, err := fmt.Fprint(w, ":heartbeat\n\n"); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !writeSSE(s.sessionSnapshotStreamEvent(sess)) {
		return
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			if !writeSSE(event) {
				return
			}
		case <-ticker.C:
			if !writeHeartbeat() {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) sessionSnapshotStreamEvent(sess *session.Session) ChatStreamEvent {
	if sess == nil {
		return ChatStreamEvent{Type: "status"}
	}
	return ChatStreamEvent{
		Type:     "session_snapshot",
		Status:   string(sess.Status),
		Messages: s.messagesToResponse(sess.Messages),
	}
}
