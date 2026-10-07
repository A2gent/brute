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
	sess, err := s.sessionManager.GetForDisplay(sessionID, true)
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

	persistedTurns := map[string]bool{}
	for _, m := range sess.Messages {
		if id, ok := m.Metadata["runtime_turn_id"].(string); ok && id != "" {
			persistedTurns[id] = true
		}
	}
	events, unsubscribe := s.subscribeSessionEventsWithReplay(sessionID, persistedTurns)
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

	snapshot := s.sessionSnapshotStreamEvent(sess)
	// Caesar may already have fetched this exact persisted transcript. Keep the
	// handshake and replay, but omit duplicate history only for an exact version.
	if r.URL.Query().Get("after_updated_at") == sess.UpdatedAt.Format(time.RFC3339Nano) {
		snapshot.Messages = nil
	}
	if !writeSSE(snapshot) {
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
	routedProvider, routedModel := sessionRoutedProviderAndModel(sess)
	routedRule, routedReason := sessionRoutingRuleAndReason(sess)
	fallbackActiveProvider, fallbackActiveModel := sessionFallbackActiveProviderAndModel(sess)
	return ChatStreamEvent{
		Type:                   "session_snapshot",
		Status:                 string(sess.Status),
		Messages:               s.messagesToResponse(sess.Messages),
		RoutedProvider:         routedProvider,
		RoutedModel:            routedModel,
		RoutedRule:             routedRule,
		RoutedReason:           routedReason,
		FallbackActiveProvider: fallbackActiveProvider,
		FallbackActiveModel:    fallbackActiveModel,
		PromptCache:            sessionPromptCache(sess),
	}
}
