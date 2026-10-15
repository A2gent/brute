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

	// fetch-based clients cannot set EventSource's header, so accept a query too.
	lastEventID := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if lastEventID == "" {
		lastEventID = strings.TrimSpace(r.URL.Query().Get("last_event_id"))
	}
	hub := s.catalogHub()
	sub := hub.subscribe(projectID, lastEventID, true)
	defer hub.unsubscribe(sub)
	events := sub.events

	writeSSE := func(event SessionCatalogEvent) bool {
		payload, err := json.Marshal(event)
		if err != nil {
			return true
		}
		idLine := ""
		if event.ID != "" {
			idLine = "id: " + event.ID + "\n"
		}
		if _, err := fmt.Fprintf(w, "%sevent: %s\ndata: %s\n\n", idLine, event.Type, payload); err != nil {
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
