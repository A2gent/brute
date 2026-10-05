package http

// maxLiveTurnEvents bounds the replay buffer of a single in-flight LLM turn.
const maxLiveTurnEvents = 2000

// isLiveTurnEvent reports whether an event carries in-flight turn content that is
// not persisted until the turn ends (streamed text and native runtime tool events).
func isLiveTurnEvent(event ChatStreamEvent) bool {
	if event.TurnID == "" {
		return false
	}
	switch event.Type {
	case "assistant_delta", "reasoning_delta", "tool_started", "tool_updated",
		"tool_input_completed", "tool_output", "cost", "runtime_warning":
		return true
	case "tool_completed":
		return event.RuntimeTool != nil
	}
	return false
}

// bufferLiveTurnEvent records in-flight turn events so a subscriber that opens the
// session mid-turn can replay them. The buffer is dropped once the turn is persisted
// (tool_executing / step_completed / done / error). Caller must hold sessionEventsMu.
func (s *Server) bufferLiveTurnEvent(sessionID string, event ChatStreamEvent) {
	switch event.Type {
	case "tool_executing", "step_completed", "done", "error":
		delete(s.liveTurnEvents, sessionID)
		return
	}
	if !isLiveTurnEvent(event) {
		return
	}
	if s.liveTurnEvents == nil {
		s.liveTurnEvents = make(map[string][]ChatStreamEvent)
	}
	buf := s.liveTurnEvents[sessionID]
	if n := len(buf); n > 0 && (event.Type == "assistant_delta" || event.Type == "reasoning_delta") &&
		buf[n-1].Type == event.Type && buf[n-1].TurnID == event.TurnID {
		buf[n-1].Delta += event.Delta
		return
	}
	if len(buf) >= maxLiveTurnEvents {
		return
	}
	s.liveTurnEvents[sessionID] = append(buf, event)
}

func (s *Server) SubscribeSessionEvents(sessionID string) (<-chan ChatStreamEvent, func()) {
	return s.subscribeSessionEventsWithReplay(sessionID, nil)
}

// subscribeSessionEventsWithReplay also pre-fills the channel with buffered events of
// the in-flight turn, skipping turns already present in the persisted snapshot
// (persistedTurns) to avoid duplicating their text.
func (s *Server) subscribeSessionEventsWithReplay(sessionID string, persistedTurns map[string]bool) (<-chan ChatStreamEvent, func()) {
	events := make(chan ChatStreamEvent, 128)
	if s == nil || sessionID == "" {
		close(events)
		return events, func() {}
	}

	s.sessionEventsMu.Lock()
	for _, ev := range s.liveTurnEvents[sessionID] {
		if persistedTurns[ev.TurnID] {
			continue
		}
		select {
		case events <- ev:
		default:
		}
	}
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
	s.bufferLiveTurnEvent(sessionID, event)
	for ch := range s.sessionEventSubs[sessionID] {
		select {
		case ch <- event:
		default:
		}
	}
}
