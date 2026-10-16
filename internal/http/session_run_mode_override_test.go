package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/go-chi/chi/v5"
)

func runModeOverrideRequest(s *Server, id string, mode string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(sessionRunModeOverrideRequest{QueueMode: mode})
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+id+"/run-mode", bytes.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("sessionID", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
	response := httptest.NewRecorder()
	s.handleOverrideSessionRunMode(response, req)
	return response
}

func autoRunModeSession(t *testing.T, s *Server, queued bool) *session.Session {
	t.Helper()
	var sess *session.Session
	var err error
	if queued {
		sess, err = s.sessionManager.CreateQueued("build")
	} else {
		sess, err = s.sessionManager.Create("build")
	}
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("Inspect the repository")
	sess.Metadata[session.RunModeDecisionKey] = session.RunModeDecision{Label: "read_only", Confidence: 0.93, Source: "jev"}
	sess.Metadata[sessionQueuePausedKey] = true
	if err := s.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestOverrideAutoRunModeToSerialQueue(t *testing.T) {
	s, _ := newUnifiedAgentsTestServer(t)
	sess := autoRunModeSession(t, s, false)

	response := runModeOverrideRequest(s, sess.ID, "serial")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	updated, err := s.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != session.StatusQueued || sessionQueueMode(updated) != sessionQueueModeSerial || !sessionQueueAutoStart(updated) {
		t.Fatalf("queue override not persisted: status=%s metadata=%v", updated.Status, updated.Metadata)
	}
	if updated.RunModeDecision() == nil || updated.RunModeDecision().Source != "jev" || updated.Metadata["run_mode_override"] != "serial" {
		t.Fatalf("automatic evidence or override missing: metadata=%v", updated.Metadata)
	}
}

func TestOverrideAutoRunModeToNow(t *testing.T) {
	s, _ := newUnifiedAgentsTestServer(t)
	sess := autoRunModeSession(t, s, true)
	sess.Metadata[sessionQueueModeMetadataKey] = sessionQueueModeSerial
	sess.Metadata[sessionQueueAutoStartKey] = true
	if err := s.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}

	response := runModeOverrideRequest(s, sess.ID, "now")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	updated, err := s.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != session.StatusRunning || sessionQueueMode(updated) != "" || sessionQueueAutoStart(updated) {
		t.Fatalf("now override not persisted: status=%s metadata=%v", updated.Status, updated.Metadata)
	}
	if updated.RunModeDecision() == nil || updated.RunModeDecision().Source != "jev" || updated.Metadata["run_mode_override"] != "now" {
		t.Fatalf("automatic evidence or override missing: metadata=%v", updated.Metadata)
	}
}

func TestOverrideAutoRunModeRejectsActiveSessionRun(t *testing.T) {
	s, _ := newUnifiedAgentsTestServer(t)
	sess := autoRunModeSession(t, s, false)
	_, release, err := s.sessionManager.AcquireRun(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	response := runModeOverrideRequest(s, sess.ID, "serial")
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want conflict for active run", response.Code, response.Body.String())
	}
}

func TestOverrideAutoRunModeRejectsManualDecision(t *testing.T) {
	s, _ := newUnifiedAgentsTestServer(t)
	sess := autoRunModeSession(t, s, false)
	sess.Metadata[session.RunModeDecisionKey] = session.RunModeDecision{Label: "now", Confidence: 1, Source: "manual"}
	if err := s.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}

	response := runModeOverrideRequest(s, sess.ID, "serial")
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want conflict for manual decision", response.Code, response.Body.String())
	}
}
