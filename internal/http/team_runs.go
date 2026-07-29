package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/teamdef"
	"github.com/go-chi/chi/v5"
)

type teamRunMemberResponse struct {
	Role      string `json:"role"`
	AgentRef  string `json:"agent_ref"`
	SessionID string `json:"session_id"`
}

type teamRunResponse struct {
	ID         string                  `json:"id"`
	TeamID     string                  `json:"team_id"`
	SessionID  string                  `json:"session_id"`
	Status     string                  `json:"status"`
	StopReason string                  `json:"stop_reason,omitempty"`
	Policy     *teamdef.Policy         `json:"policy,omitempty"`
	Members    []teamRunMemberResponse `json:"members"`
	StartedAt  time.Time               `json:"started_at"`
	EndedAt    *time.Time              `json:"ended_at,omitempty"`
}

type teamMessageResponse struct {
	ID           string            `json:"id"`
	TeamRunID    string            `json:"team_run_id"`
	ThreadID     string            `json:"thread_id"`
	From         string            `json:"from"`
	To           []string          `json:"to"`
	CC           []string          `json:"cc,omitempty"`
	Kind         string            `json:"kind"`
	Subject      string            `json:"subject,omitempty"`
	Body         string            `json:"body"`
	ExpectsReply bool              `json:"expects_reply"`
	CreatedAt    time.Time         `json:"created_at"`
	Delivered    map[string]string `json:"delivered,omitempty"`
}

func (s *Server) registerTeamRunRoutes(r chi.Router) {
	r.Route("/team-runs", func(r chi.Router) {
		r.Get("/{runID}", s.handleGetTeamRun)
		r.Get("/{runID}/messages", s.handleListTeamRunMessages)
	})
	r.Get("/sessions/{sessionID}/team-run", s.handleGetSessionTeamRun)
}

func (s *Server) handleGetTeamRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetTeamRun(chi.URLParam(r, "runID"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, storage.ErrTeamRunNotFound) {
			status = http.StatusNotFound
		}
		s.errorResponse(w, status, err.Error())
		return
	}
	response, err := s.teamRunResponseFromStore(run)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleGetSessionTeamRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetTeamRunBySession(chi.URLParam(r, "sessionID"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, storage.ErrTeamRunNotFound) {
			status = http.StatusNotFound
		}
		s.errorResponse(w, status, err.Error())
		return
	}
	response, err := s.teamRunResponseFromStore(run)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleListTeamRunMessages(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	if _, err := s.store.GetTeamRun(runID); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, storage.ErrTeamRunNotFound) {
			status = http.StatusNotFound
		}
		s.errorResponse(w, status, err.Error())
		return
	}
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, "limit must be an integer")
			return
		}
		limit = parsed
	}
	messages, err := s.store.ListTeamMessages(runID, after, limit)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list team messages: "+err.Error())
		return
	}
	responses := make([]teamMessageResponse, 0, len(messages))
	for _, message := range messages {
		responses = append(responses, teamMessageResponseFromStore(message))
	}
	s.jsonResponse(w, http.StatusOK, responses)
}

func (s *Server) teamRunResponseFromStore(run *storage.TeamRun) (teamRunResponse, error) {
	if run == nil {
		return teamRunResponse{}, errors.New("team run is empty")
	}
	members, err := s.store.ListTeamRunMembers(run.ID)
	if err != nil {
		return teamRunResponse{}, err
	}
	memberResponses := make([]teamRunMemberResponse, 0, len(members))
	for _, member := range members {
		memberResponses = append(memberResponses, teamRunMemberResponse{
			Role:      member.Role,
			AgentRef:  member.AgentRef,
			SessionID: member.SessionID,
		})
	}
	response := teamRunResponse{
		ID:         run.ID,
		TeamID:     run.TeamID,
		SessionID:  run.SessionID,
		Status:     run.Status,
		StopReason: run.StopReason,
		Members:    memberResponses,
		StartedAt:  run.StartedAt,
		EndedAt:    run.EndedAt,
	}
	if strings.TrimSpace(run.PolicyJSON) != "" {
		var policy teamdef.Policy
		if err := json.Unmarshal([]byte(run.PolicyJSON), &policy); err != nil {
			return teamRunResponse{}, err
		}
		response.Policy = &policy
	}
	return response, nil
}

func teamMessageResponseFromStore(message *storage.TeamMessage) teamMessageResponse {
	delivered := map[string]string{}
	for role, at := range message.Delivered {
		delivered[role] = at.UTC().Format(time.RFC3339Nano)
	}
	return teamMessageResponse{
		ID:           message.ID,
		TeamRunID:    message.TeamRunID,
		ThreadID:     message.ThreadID,
		From:         message.FromRole,
		To:           message.ToRoles,
		CC:           message.CCRoles,
		Kind:         message.Kind,
		Subject:      message.Subject,
		Body:         message.Body,
		ExpectsReply: message.ExpectsReply,
		CreatedAt:    message.CreatedAt,
		Delivered:    delivered,
	}
}
