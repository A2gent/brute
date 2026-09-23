package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleSessionCatalogEvents(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "Streaming is not supported by the server")
		return
	}

	events, unsubscribe := s.SubscribeSessionCatalogEvents(projectID)
	defer unsubscribe()

	writeSSE := func(event SessionCatalogEvent) bool {
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

	if !writeSSE(SessionCatalogEvent{Type: "catalog_ready"}) {
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
