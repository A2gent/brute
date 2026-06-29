package http

func (s *Server) SubscribeSessionEvents(sessionID string) (<-chan ChatStreamEvent, func()) {
	events := make(chan ChatStreamEvent, 128)
	if s == nil || sessionID == "" {
		close(events)
		return events, func() {}
	}

	s.sessionEventsMu.Lock()
	if s.sessionEventSubs == nil {
		s.sessionEventSubs = make(map[string]map[chan ChatStreamEvent]struct{})
	}
	subs := s.sessionEventSubs[sessionID]
	if subs == nil {
		subs = make(map[chan ChatStreamEvent]struct{})
		s.sessionEventSubs[sessionID] = subs
	}
	subs[events] = struct{}{}
	s.sessionEventsMu.Unlock()

	unsubscribe := func() {
		s.sessionEventsMu.Lock()
		defer s.sessionEventsMu.Unlock()
		subs := s.sessionEventSubs[sessionID]
		if subs == nil {
			return
		}
		if _, ok := subs[events]; !ok {
			return
		}
		delete(subs, events)
		close(events)
		if len(subs) == 0 {
			delete(s.sessionEventSubs, sessionID)
		}
	}

	return events, unsubscribe
}

func (s *Server) publishSessionEvent(sessionID string, event ChatStreamEvent) {
	if s == nil || sessionID == "" || event.Type == "" || event.Type == "heartbeat" {
		return
	}

	s.sessionEventsMu.Lock()
	defer s.sessionEventsMu.Unlock()
	for ch := range s.sessionEventSubs[sessionID] {
		select {
		case ch <- event:
		default:
		}
	}
}
