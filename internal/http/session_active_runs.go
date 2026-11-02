package http

import (
	"context"
	"errors"
	"strings"
	"time"

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

const answerRunDrainTimeout = 30 * time.Second

func (s *Server) waitForSessionRunsToEnd(ctx context.Context, sessionID string, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for s.activeSessionRunCount(sessionID) > 0 {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
	return true
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
		// The last run ended on its own (cancel removes the entry first, so a user
		// stop never lands here): deliver anything the user queued meanwhile.
		go s.drainSessionPendingMessages(sessionID)
	}
}

func (s *Server) cancelActiveSessionRuns(sessionID string) int {
	s.delegationJobs.cancelParent(sessionID)
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
