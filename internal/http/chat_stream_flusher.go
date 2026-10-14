package http

import (
	"sync"
	"time"
)

const (
	chatStreamFlushWindow = 20 * time.Millisecond
	chatStreamFlushBytes  = 4096
)

// chatStreamFlusher serializes timer, heartbeat and agent callbacks. A type/turn
// change or any non-delta event is a barrier: pending text must be delivered first.
// The sink is still called after a socket failure so session subscribers keep
// receiving the run even when the initiating tab disconnects.
type chatStreamFlusher struct {
	mu       sync.Mutex
	sink     func(ChatStreamEvent) bool
	window   time.Duration
	maxBytes int
	pending  *bufferedStreamEvent
	timer    *time.Timer
	writable bool
	closed   bool
}

func newChatStreamFlusher(sink func(ChatStreamEvent) bool, window time.Duration, maxBytes int) *chatStreamFlusher {
	return &chatStreamFlusher{sink: sink, window: window, maxBytes: maxBytes, writable: true}
}

func (s *chatStreamFlusher) emit(event ChatStreamEvent) {
	if !s.sink(event) {
		s.writable = false
	}
}

// Caller holds mu. AfterFunc callbacks may already be waiting for it, so they
// check the timer identity instead of flushing a newer batch after Stop loses a race.
func (s *chatStreamFlusher) flush() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.pending != nil {
		event := s.pending.snapshot()
		s.pending = nil
		s.emit(event)
	}
}

func (s *chatStreamFlusher) Write(event ChatStreamEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if !isTextDelta(event) {
		s.flush()
		s.emit(event)
		return s.writable
	}
	if s.pending != nil && !canMergeTextDelta(s.pending.event, event) {
		s.flush()
	}
	if s.pending == nil {
		s.pending = newBufferedStreamEvent(event)
		var timer *time.Timer
		timer = time.AfterFunc(s.window, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.closed && s.timer == timer {
				s.flush()
			}
		})
		s.timer = timer
	} else {
		s.pending.text.WriteString(event.Delta)
	}
	if s.pending.text.Len() >= s.maxBytes {
		s.flush()
	}
	return s.writable
}

func (s *chatStreamFlusher) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.flush()
}
