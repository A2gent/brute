package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const providerTransitionModeSetting = "A2GENT_PROVIDER_TRANSITION_MODE"

type createProviderTransitionRequest struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
}

func (s *Server) transitionMode() string {
	mode := s.config.ProviderTransitionMode
	if s.store != nil {
		if settings, err := s.store.GetSettings(); err == nil && settings[providerTransitionModeSetting] != "" {
			mode = settings[providerTransitionModeSetting]
		}
	}
	if mode == "" {
		mode = session.DefaultTransitionMode
	}
	return mode
}
func (s *Server) resolveTransitionTarget(sess *session.Session, endpoint session.ProviderEndpoint) (llm.Client, int, error) {
	ref := config.NormalizeProviderRef(endpoint.Provider)
	if ref == string(config.ProviderFallback) || ref == string(config.ProviderAutoRouter) || config.IsFallbackAggregateRef(ref) || config.IsClassifierProvider(ref) {
		return nil, 0, fmt.Errorf("transition requires a concrete chat provider")
	}
	if err := s.validateProviderRefForExecution(ref); err != nil {
		return nil, 0, err
	}
	client, err := s.createBaseLLMClientForSession(config.ProviderType(ref), endpoint.Model, sess)
	return client, s.resolveContextWindowForProvider(config.ProviderType(ref), endpoint.Model), err
}
func (s *Server) publishTransition(sess *session.Session, t *session.ProviderTransition) {
	s.publishSessionEvent(sess.ID, ChatStreamEvent{Type: "provider_transition", Transition: t, Status: string(sess.Status)})
	s.publishSessionCatalog("session_updated", sess)
}
func (s *Server) handleListProviderTransitions(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessionManager.Get(chi.URLParam(r, "sessionID"))
	if err != nil {
		s.errorResponse(w, 404, "Session not found")
		return
	}
	ts := session.ProviderTransitions(sess)
	if ts == nil {
		ts = []session.ProviderTransition{}
	}
	s.jsonResponse(w, 200, map[string]interface{}{"version": 1, "transitions": ts})
}
func (s *Server) handleCreateProviderTransition(w http.ResponseWriter, r *http.Request) {
	var req createProviderTransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, 400, "Invalid transition body")
		return
	}
	s.createProviderTransition(w, r, req)
}
func (s *Server) createProviderTransition(w http.ResponseWriter, r *http.Request, req createProviderTransitionRequest) {
	id := chi.URLParam(r, "sessionID")
	ctx, release, err := s.sessionManager.AcquireRun(s.sessionRunParentContext(), id)
	if err != nil || s.activeSessionRunCount(id) > 0 {
		if release != nil {
			release()
		}
		s.errorResponse(w, 409, "Wait for the active run to reach a safe boundary")
		return
	}
	transferred := false
	defer func() {
		if !transferred {
			release()
		}
	}()
	sess, err := s.sessionManager.Get(id)
	if err != nil {
		s.errorResponse(w, 404, "Session not found")
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Provider = config.NormalizeProviderRef(req.Provider)
	if req.ID == "" || len(req.ID) > 128 || req.Provider == "" {
		s.errorResponse(w, 400, "id and provider are required (id max 128 bytes)")
		return
	}
	if req.Model == "" {
		req.Model = s.resolveModelForProvider(config.ProviderType(req.Provider))
	}
	// Retries retain the captured policy even when settings have since changed.
	for _, old := range session.ProviderTransitions(sess) {
		if old.ID == req.ID {
			if old.Target.Provider != req.Provider || old.Target.Model != req.Model || (req.Mode != "" && old.Mode != req.Mode) {
				s.errorResponse(w, 409, "Transition ID reused with different parameters")
				return
			}
			s.jsonResponse(w, 200, old)
			return
		}
	}
	if old := session.CurrentProviderTransition(sess); old != nil && !old.Terminal() {
		s.errorResponse(w, 409, "Resolve the existing transition first")
		return
	}
	if isDelegatedSession(sess) {
		s.errorResponse(w, 409, "Delegated agents cannot create linked/provider continuations")
		return
	}
	if req.Mode == "" {
		req.Mode = s.transitionMode()
	}
	sourceProvider := s.resolveSessionProviderType(sess)
	source := session.ProviderEndpoint{Provider: string(sourceProvider), Model: s.resolveSessionModel(sess, sourceProvider)}
	if p, _ := sess.Metadata[fallbackActiveProviderMetadataKey].(string); p != "" {
		source.Provider = p
		source.Model, _ = sess.Metadata[fallbackActiveModelMetadataKey].(string)
	}
	t, err := session.NewProviderTransition(req.ID, source, session.ProviderEndpoint{Provider: req.Provider, Model: req.Model}, "manual", req.Mode)
	if err != nil {
		s.errorResponse(w, 400, err.Error())
		return
	}
	// Construction validates configuration only; it must never contact the target.
	if _, _, err := s.resolveTransitionTarget(sess, t.Target); err != nil {
		s.errorResponse(w, 400, err.Error())
		return
	}
	session.PutProviderTransition(sess, t)
	if t.Confirmation == "pending" {
		sess.SetStatus(session.StatusInputRequired)
	} else {
		sess.SetStatus(session.StatusPaused)
	}
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, 500, err.Error())
		return
	}
	s.publishTransition(sess, t)
	s.jsonResponse(w, 202, t)
	if t.Confirmation != "pending" {
		transferred = true
		s.startTransitionRun(ctx, release, sess)
	}
}
func (s *Server) handleProviderTransitionDecision(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, 400, "Invalid decision body")
		return
	}
	s.changeProviderTransition(w, r, req.Decision, false)
}
func (s *Server) handleResumeProviderTransition(w http.ResponseWriter, r *http.Request) {
	s.changeProviderTransition(w, r, "", true)
}
func (s *Server) changeProviderTransition(w http.ResponseWriter, r *http.Request, decision string, resume bool) {
	id := chi.URLParam(r, "sessionID")
	transitionID := chi.URLParam(r, "transitionID")
	// An identical decision can be read during continuation without acquiring its
	// lease. It never schedules work; state mutation still requires exclusive ownership.
	sess, err := s.sessionManager.Get(id)
	if err != nil {
		s.errorResponse(w, 404, "Session not found")
		return
	}
	if !resume {
		for _, t := range session.ProviderTransitions(sess) {
			if t.ID == transitionID && ((decision == "approve" && t.Confirmation == "approved") || (decision == "reject" && t.Confirmation == "rejected")) {
				s.jsonResponse(w, 200, t)
				return
			}
		}
	}
	ctx, release, err := s.sessionManager.AcquireRun(s.sessionRunParentContext(), id)
	if err != nil || s.activeSessionRunCount(id) > 0 {
		if release != nil {
			release()
		}
		s.errorResponse(w, 409, "Session is running")
		return
	}
	transferred := false
	defer func() {
		if !transferred {
			release()
		}
	}()
	sess, err = s.sessionManager.Get(id)
	if err != nil {
		s.errorResponse(w, 404, "Session not found")
		return
	}
	t := session.CurrentProviderTransition(sess)
	if t == nil || t.ID != transitionID {
		s.errorResponse(w, 404, "Current transition not found")
		return
	}
	if resume {
		if t.Terminal() || t.Confirmation == "pending" || t.Phase == "continuing" || strings.HasPrefix(t.Error, "continuation interrupted") {
			s.errorResponse(w, 409, "Transition cannot be replayed safely")
			return
		}
		if t.Compaction.Outcome == "succeeded" && sess.Metadata["provider_context_epoch"] != nil {
			t.Phase = "ready"
		} else if t.Phase != "ready" {
			t.Phase = "requested"
		}
		t.Error = ""
	} else {
		changed, err := t.Decide(decision)
		if err != nil {
			s.errorResponse(w, 409, err.Error())
			return
		}
		if !changed {
			s.jsonResponse(w, 200, t)
			return
		}
	}
	sess.SetStatus(session.StatusPaused)
	session.PutProviderTransition(sess, t)
	if err := s.sessionManager.Save(sess); err != nil {
		s.errorResponse(w, 500, err.Error())
		return
	}
	s.publishTransition(sess, t)
	s.jsonResponse(w, 200, t)
	if resume || decision == "approve" {
		transferred = true
		s.startTransitionRun(ctx, release, sess)
	}
}
func (s *Server) startTransitionRun(ctx context.Context, release func(), sess *session.Session) {
	ctx, cancel := context.WithCancel(ctx)
	runID := s.registerActiveSessionRun(sess.ID, cancel)
	go func() {
		defer func() { cancel(); release(); s.unregisterActiveSessionRun(sess.ID, runID) }()
		// Resolve the target for an approved manual transition without depending on
		// a failed/unconfigured source. The agent still enforces consent before Chat.
		t := session.CurrentProviderTransition(sess)
		if t != nil && t.Cause == "manual" {
			client, window, err := s.resolveTransitionTarget(sess, t.Target)
			if err != nil {
				t.Phase = "paused"
				t.Error = err.Error()
				session.PutProviderTransition(sess, t)
				_ = s.sessionManager.Save(sess)
				s.publishTransition(sess, t)
				return
			}
			cfg := agent.Config{Name: sess.AgentID, Provider: t.Source.Provider, Model: t.Source.Model, ContextWindow: window, SystemPrompt: s.buildSystemPromptForSession(sess), MaxSteps: s.config.MaxSteps, Temperature: s.config.Temperature}
			ag := s.newAgentFromConfig(cfg, client, s.toolManagerForSession(sess))
			_, _, err = ag.RunWithEvents(ctx, sess, "", func(ev agent.Event) {
				if event, ok := s.agentEventToStreamEvent(sess, config.ProviderType(t.Target.Provider), ev); ok {
					s.publishSessionEvent(sess.ID, event)
				}
			})
			if err != nil && !errors.Is(err, agent.ErrTransitionPaused) {
				s.publishSessionEvent(sess.ID, ChatStreamEvent{Type: "error", Error: err.Error()})
			}
			s.publishSessionCatalog("session_updated", sess)
			if linked := session.CurrentProviderTransition(sess); linked != nil && linked.Phase == "linked" {
				if child, e := s.sessionManager.Get(linked.LinkedSessionID); e == nil && child.Status == session.StatusQueued {
					if childCtx, childRelease, e := s.sessionManager.AcquireRun(s.sessionRunParentContext(), child.ID); e == nil {
						s.startTransitionRun(childCtx, childRelease, child)
					}
				}
			}
			return
		}
		result, err := s.runSessionWithoutStreaming(ctx, sess, "")
		_ = s.finalizeSessionRunWithoutStreaming(ctx, sess, result, err)
	}()
}
func (s *Server) handleUpdateSessionProviderTransition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Mode     string `json:"mode"`
		ID       string `json:"transition_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, 400, "Invalid provider body")
		return
	}
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	s.createProviderTransition(w, r, createProviderTransitionRequest{ID: req.ID, Provider: req.Provider, Model: req.Model, Mode: req.Mode})
}

// Startup never replays an external request. Pre-submission phases remain
// resumable; a continuing checkpoint needs human inspection for native effects.
func (s *Server) recoverProviderTransitions() {
	sessions, err := s.sessionManager.List()
	if err != nil {
		return
	}
	for _, sess := range sessions {
		t := session.CurrentProviderTransition(sess)
		if t == nil || t.Terminal() {
			continue
		}
		switch t.Phase {
		case "continuing":
			t.Error = "continuation interrupted by server restart; inspect tools before starting a new run"
			t.Phase = "paused"
		case "compacting":
			t.Error = "compaction interrupted by server restart; original context preserved"
			t.Phase = "paused"
		}
		if t.Confirmation == "pending" {
			sess.SetStatus(session.StatusInputRequired)
		} else {
			sess.SetStatus(session.StatusPaused)
		}
		session.PutProviderTransition(sess, t)
		_ = s.sessionManager.Save(sess)
	}
}
