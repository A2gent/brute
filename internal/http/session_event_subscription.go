package http

import (
	"strings"
	"sync"
)

// bufferedStreamEvent is always used by pointer: a non-empty Builder must not be
// copied. String snapshots stay immutable while subsequent deltas are appended.
type bufferedStreamEvent struct {
	event ChatStreamEvent
	text  strings.Builder
}

func newBufferedStreamEvent(event ChatStreamEvent) *bufferedStreamEvent {
	buffered := &bufferedStreamEvent{event: event}
	if isTextDelta(event) {
		buffered.text.WriteString(event.Delta)
		buffered.event.Delta = ""
	}
	return buffered
}

func isTextDelta(event ChatStreamEvent) bool {
	return event.Type == "assistant_delta" || event.Type == "reasoning_delta"
}

func canMergeTextDelta(tail, event ChatStreamEvent) bool {
	if !isTextDelta(event) || tail.Type != event.Type || tail.TurnID != event.TurnID || tail.Step != event.Step {
		return false
	}
	if tail.Provenance == nil || event.Provenance == nil {
		return tail.Provenance == event.Provenance
	}
	return *tail.Provenance == *event.Provenance
}

func (b *bufferedStreamEvent) snapshot() ChatStreamEvent {
	event := b.event
	if isTextDelta(event) {
		event.Delta = b.text.String()
	}
	return event
}

// A subscriber owns its backlog, so a slow socket cannot block a run or another
// subscriber. Only adjacent text deltas may be merged; state transitions must not
// be discarded when the output channel fills. Unsubscribe releases the backlog.
type sessionEventSubscription struct {
	events  chan ChatStreamEvent
	mu      sync.Mutex
	pending []*bufferedStreamEvent
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
}

func newSessionEventSubscription() *sessionEventSubscription {
	sub := &sessionEventSubscription{
		events: make(chan ChatStreamEvent, 128),
		wake:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go sub.deliver()
	return sub
}

func (s *sessionEventSubscription) enqueue(event ChatStreamEvent) {
	s.mu.Lock()
	if n := len(s.pending); n > 0 && canMergeTextDelta(s.pending[n-1].event, event) {
		s.pending[n-1].text.WriteString(event.Delta)
	} else {
		s.pending = append(s.pending, newBufferedStreamEvent(event))
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *sessionEventSubscription) deliver() {
	defer close(s.done)
	defer close(s.events)
	for {
		s.mu.Lock()
		if len(s.pending) == 0 {
			s.mu.Unlock()
			select {
			case <-s.wake:
				continue
			case <-s.stop:
				return
			}
		}
		event := s.pending[0].snapshot()
		s.pending[0] = nil
		s.pending = s.pending[1:]
		if len(s.pending) == 0 {
			s.pending = nil
		}
		s.mu.Unlock()
		select {
		case s.events <- event:
		case <-s.stop:
			return
		}
	}
}
