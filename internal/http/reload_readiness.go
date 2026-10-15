package http

import "net/http"

type reloadReadinessResponse struct {
	Idle               bool `json:"idle"`
	ActiveRuns         int  `json:"active_runs"`
	ActiveQueueWorkers int  `json:"active_queue_workers"`
}

// handleReloadReadiness lets the dev hot-reloader (cmd/devreload) defer restarts
// while work is in flight. A serial queue worker only lives while its scope has
// queued sessions that are about to start; it exits when the queue stalls, so a
// stuck queue cannot block reloads forever.
func (s *Server) handleReloadReadiness(w http.ResponseWriter, r *http.Request) {
	s.serialQueueMu.Lock()
	workers := len(s.serialQueueWorkers)
	s.serialQueueMu.Unlock()
	runs := s.sessionManager.ActiveRunCount()

	s.jsonResponse(w, http.StatusOK, reloadReadinessResponse{
		Idle:               runs == 0 && workers == 0,
		ActiveRuns:         runs,
		ActiveQueueWorkers: workers,
	})
}
