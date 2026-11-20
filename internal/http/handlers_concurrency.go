package http

import (
	"encoding/json"
	"net/http"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

type concurrencyResponse struct {
	Config        concurrencyConfig `json:"config"`
	Usage         concurrencyUsage  `json:"usage"`
	Queued        int               `json:"queued"`
	Resources     resourceReading   `json:"resources"`
	BlockedReason string            `json:"blocked_reason,omitempty"`
}

func (s *Server) concurrencyStatus() concurrencyResponse {
	cfg := s.loadConcurrencyConfig()
	reading := s.currentResourceReading()
	return concurrencyResponse{
		Config:        cfg,
		Usage:         s.currentConcurrencyUsage(),
		Queued:        s.countAutoQueuedSessions(),
		Resources:     reading,
		BlockedReason: resourceBlockReasonFor(cfg, reading),
	}
}

func (s *Server) countAutoQueuedSessions() int {
	if s.sessionManager == nil {
		return 0
	}
	sessions, err := s.sessionManager.List()
	if err != nil {
		logging.Warn("Failed to count queued sessions: %v", err)
		return 0
	}
	count := 0
	for _, sess := range sessions {
		if sess != nil && sess.Status == session.StatusQueued && sessionIsSerialQueuedAutoRun(sess) && !sessionIsQueuePaused(sess) && sessionArchivedAt(sess) == nil {
			count++
		}
	}
	return count
}

func (s *Server) handleGetConcurrency(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, s.concurrencyStatus())
}

func (s *Server) handleUpdateConcurrency(w http.ResponseWriter, r *http.Request) {
	var req concurrencyConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	cfg, err := normalizeConcurrencyConfig(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveConcurrencyConfig(cfg); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save concurrency settings: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, s.concurrencyStatus())
}
