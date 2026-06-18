package http

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

func (s *Server) registerActiveSessionRun(sessionID string, cancel context.CancelFunc) string {
	runID := uuid.New().String()
	s.activeRunsMu.Lock()
	defer s.activeRunsMu.Unlock()

	if s.activeRuns == nil {
		s.activeRuns = make(map[string]map[string]context.CancelFunc)
	}
	runs, ok := s.activeRuns[sessionID]
	if !ok {
		runs = make(map[string]context.CancelFunc)
		s.activeRuns[sessionID] = runs
	}
	runs[runID] = cancel
	return runID
}

func (s *Server) activeSessionRunCount(sessionID string) int {
	if s == nil || sessionID == "" {
		return 0
	}
	s.activeRunsMu.Lock()
	defer s.activeRunsMu.Unlock()
	return len(s.activeRuns[sessionID])
}

func (s *Server) unregisterActiveSessionRun(sessionID, runID string) {
	s.activeRunsMu.Lock()
	defer s.activeRunsMu.Unlock()

	runs, ok := s.activeRuns[sessionID]
	if !ok {
		return
	}
	delete(runs, runID)
	if len(runs) == 0 {
		delete(s.activeRuns, sessionID)
	}
}

func (s *Server) cancelActiveSessionRuns(sessionID string) int {
	s.activeRunsMu.Lock()
	runs, ok := s.activeRuns[sessionID]
	if !ok || len(runs) == 0 {
		s.activeRunsMu.Unlock()
		return 0
	}
	cancels := make([]context.CancelFunc, 0, len(runs))
	for _, cancel := range runs {
		cancels = append(cancels, cancel)
	}
	delete(s.activeRuns, sessionID)
	s.activeRunsMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

func isCancellationError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "context canceled") || strings.Contains(lower, "cancelled")
}
