package http

import "github.com/A2gent/brute/internal/session"

// SessionCatalogEvent is a lightweight lifecycle notice for the sessions list.
// Caesar uses this so sessions created outside the current tab appear without reload.
type SessionCatalogEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Title     string `json:"title,omitempty"`
}

func (s *Server) SubscribeSessionCatalogEvents(projectID string) (<-chan SessionCatalogEvent, func()) {
	events := make(chan SessionCatalogEvent, 128)
	if s == nil {
		close(events)
		return events, func() {}
	}

	s.sessionCatalogMu.Lock()
	if s.sessionCatalogSubs == nil {
		s.sessionCatalogSubs = make(map[chan SessionCatalogEvent]string)
	}
	s.sessionCatalogSubs[events] = projectID
	s.sessionCatalogMu.Unlock()

	unsubscribe := func() {
		s.sessionCatalogMu.Lock()
		defer s.sessionCatalogMu.Unlock()
		if _, ok := s.sessionCatalogSubs[events]; !ok {
			return
		}
		delete(s.sessionCatalogSubs, events)
		close(events)
	}

	return events, unsubscribe
}

func (s *Server) publishSessionCatalogEvent(event SessionCatalogEvent) {
	if s == nil || event.Type == "" || event.Type == "heartbeat" || event.Type == "catalog_ready" {
		return
	}

	s.sessionCatalogMu.Lock()
	defer s.sessionCatalogMu.Unlock()
	for ch, projectID := range s.sessionCatalogSubs {
		if projectID != "" && event.ProjectID != projectID {
			continue
		}
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *Server) publishSessionCatalog(eventType string, sess *session.Session) {
	if s == nil || sess == nil || eventType == "" {
		return
	}
	s.publishSessionCatalogEvent(SessionCatalogEvent{
		Type:      eventType,
		SessionID: sess.ID,
		ProjectID: sessionProjectID(sess),
		Status:    string(sess.Status),
		Title:     sess.Title,
	})
}
