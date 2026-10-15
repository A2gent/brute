package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm/jev"
)

// A cold LM Studio model load can take a while; the probe exists to measure exactly that.
const systemOneProbeTimeout = 3 * time.Minute

type systemOneProbeRequest struct {
	Backend  string            `json:"backend"`
	Model    string            `json:"model"`
	Text     string            `json:"text"`
	Question string            `json:"question"`
	Type     string            `json:"type"`
	Criteria map[string]string `json:"criteria"`
}

type systemOneProbeResponse struct {
	Backend    string      `json:"backend"`
	Model      string      `json:"model,omitempty"`
	DurationMs int64       `json:"duration_ms"`
	Answer     *jev.Answer `json:"answer,omitempty"`
	Usage      *jev.Usage  `json:"usage,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// handleTestSystemOne runs one classification through the (possibly unsaved) backend/model
// selected on the System One settings page and reports the answer and wall-clock latency.
func (s *Server) handleTestSystemOne(w http.ResponseWriter, r *http.Request) {
	var req systemOneProbeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Text) == "" || strings.TrimSpace(req.Question) == "" {
		s.errorResponse(w, http.StatusBadRequest, "text and question are required")
		return
	}
	question := jev.Question{Type: req.Type, Instructions: req.Question}
	switch req.Type {
	case "choice":
		if len(req.Criteria) < 2 {
			s.errorResponse(w, http.StatusBadRequest, "choice needs at least 2 options")
			return
		}
		question.Criteria = req.Criteria
	case "score":
		// Jev takes score levels as an ordered array; keys must be 0..n-1 like the classify tool.
		levels := make([]string, len(req.Criteria))
		for i := range levels {
			level, ok := req.Criteria[strconv.Itoa(i)]
			if !ok {
				s.errorResponse(w, http.StatusBadRequest, "score levels must be keyed 0..n-1")
				return
			}
			levels[i] = level
		}
		if len(levels) < 2 {
			s.errorResponse(w, http.StatusBadRequest, "score needs at least 2 levels")
			return
		}
		question.Criteria = levels
	case "noul":
		if len(req.Criteria) > 0 {
			question.Criteria = req.Criteria
		}
	default:
		s.errorResponse(w, http.StatusBadRequest, "type must be choice, score or noul")
		return
	}

	backend := systemOneBackendJev
	if strings.EqualFold(strings.TrimSpace(req.Backend), systemOneBackendLMStudio) {
		backend = systemOneBackendLMStudio
	}
	out := systemOneProbeResponse{Backend: backend, Model: strings.TrimSpace(req.Model)}
	client, _ := s.systemOneClientFor(backend, out.Model)
	if client == nil {
		out.Error = "backend is not configured (missing Jev credentials or LM Studio model)"
		s.jsonResponse(w, http.StatusOK, out)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), systemOneProbeTimeout)
	defer cancel()
	started := time.Now()
	resp, err := client.SystemOne(ctx, jev.SystemOneRequest{State: req.Text, Questions: map[string]jev.Question{"answer": question}})
	out.DurationMs = time.Since(started).Milliseconds()
	if err != nil {
		out.Error = err.Error()
		s.jsonResponse(w, http.StatusOK, out)
		return
	}
	if resp.Model != "" {
		out.Model = resp.Model
	}
	if answer, ok := resp.Answers["answer"]; ok {
		out.Answer = &answer
	}
	out.Usage = &resp.Usage
	s.jsonResponse(w, http.StatusOK, out)
}
