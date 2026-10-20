package http

// isLiveTurnEvent reports whether an event carries in-flight turn content that is
// not persisted until the turn ends (streamed text and native runtime tool events).
func isLiveTurnEvent(event ChatStreamEvent) bool {
	if event.TurnID == "" {
		return false
	}
	switch event.Type {
	case "assistant_delta", "reasoning_delta", "tool_started", "tool_updated",
		"tool_input_completed", "tool_output", "cost", "runtime_warning",
		// MCP bridge progress for a CLI runtime tool (carries the runtime turn id), so a
		// viewer opening the session mid-turn still sees e.g. the sub-agent session link.
		"tool_progress":
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
		s.liveTurnEvents = make(map[string][]*bufferedStreamEvent)
	}
	buf := s.liveTurnEvents[sessionID]
	if n := len(buf); n > 0 && canMergeTextDelta(buf[n-1].event, event) {
		buf[n-1].text.WriteString(event.Delta)
		return
	}
	// Keep the complete unpersisted turn; truncation here cannot be recovered by
	// fetching the persisted snapshot. Adjacent text is compacted above and the
	// entire replay is released at the persistence barrier.
	s.liveTurnEvents[sessionID] = append(buf, newBufferedStreamEvent(event))
}

func (s *Server) SubscribeSessionEvents(sessionID string) (<-chan ChatStreamEvent, func()) {
	return s.subscribeSessionEventsWithReplay(sessionID, nil)
}

// subscribeSessionEventsWithReplay also pre-fills the channel with buffered events of
// the in-flight turn, skipping turns already present in the persisted snapshot
// (persistedTurns) to avoid duplicating their text.
func (s *Server) subscribeSessionEventsWithReplay(sessionID string, persistedTurns map[string]bool) (<-chan ChatStreamEvent, func()) {
	if s == nil || sessionID == "" {
		events := make(chan ChatStreamEvent)
		close(events)
		return events, func() {}
	}
	sub := newSessionEventSubscription()

	s.sessionEventsMu.Lock()
	for _, ev := range s.liveTurnEvents[sessionID] {
		if persistedTurns[ev.event.TurnID] {
			continue
		}
		sub.enqueue(ev.snapshot())
	}
	if s.sessionEventSubs == nil {
		s.sessionEventSubs = make(map[string]map[chan ChatStreamEvent]*sessionEventSubscription)
	}
	subs := s.sessionEventSubs[sessionID]
	if subs == nil {
		subs = make(map[chan ChatStreamEvent]*sessionEventSubscription)
		s.sessionEventSubs[sessionID] = subs
	}
	subs[sub.events] = sub
	s.sessionEventsMu.Unlock()

	unsubscribe := func() {
		s.sessionEventsMu.Lock()
		subs := s.sessionEventSubs[sessionID]
		if subs == nil {
			s.sessionEventsMu.Unlock()
			return
		}
		if _, ok := subs[sub.events]; !ok {
			s.sessionEventsMu.Unlock()
			return
		}
		delete(subs, sub.events)
		close(sub.stop)
		if len(subs) == 0 {
			delete(s.sessionEventSubs, sessionID)
		}
		s.sessionEventsMu.Unlock()
		<-sub.done
		sub.mu.Lock()
		sub.pending = nil
		sub.mu.Unlock()
	}

	return sub.events, unsubscribe
}

func (s *Server) publishSessionEvent(sessionID string, event ChatStreamEvent) {
	if s == nil || sessionID == "" || event.Type == "" || event.Type == "heartbeat" {
		return
	}

	s.publishSessionCatalogNotifications(sessionID, event)

	s.sessionEventsMu.Lock()
	defer s.sessionEventsMu.Unlock()
	s.bufferLiveTurnEvent(sessionID, event)
	for _, sub := range s.sessionEventSubs[sessionID] {
		sub.enqueue(event)
	}
}
