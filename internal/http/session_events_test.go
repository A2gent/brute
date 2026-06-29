package http

import (
	"testing"
	"time"
)

func TestSessionEventSubscriptionReceivesPublishedEvents(t *testing.T) {
	s := &Server{sessionEventSubs: make(map[string]map[chan ChatStreamEvent]struct{})}
	events, unsubscribe := s.SubscribeSessionEvents("session-1")
	defer unsubscribe()

	s.publishSessionEvent("session-1", ChatStreamEvent{Type: "assistant_delta", Delta: "hello"})

	select {
	case event := <-events:
		if event.Type != "assistant_delta" || event.Delta != "hello" {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
	}
}

func TestSessionEventSubscriptionIgnoresOtherSessionsAndHeartbeats(t *testing.T) {
	s := &Server{sessionEventSubs: make(map[string]map[chan ChatStreamEvent]struct{})}
	events, unsubscribe := s.SubscribeSessionEvents("session-1")
	defer unsubscribe()

	s.publishSessionEvent("session-2", ChatStreamEvent{Type: "assistant_delta", Delta: "wrong"})
	s.publishSessionEvent("session-1", ChatStreamEvent{Type: "heartbeat"})

	select {
	case event := <-events:
		t.Fatalf("unexpected event: %#v", event)
	case <-time.After(25 * time.Millisecond):
	}
}
