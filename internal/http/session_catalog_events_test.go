package http

import (
	"bytes"
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

func TestSessionCatalogSubscriptionReceivesPublishedEvents(t *testing.T) {
	s := &Server{}
	events, unsubscribe := s.SubscribeSessionCatalogEvents("")
	defer unsubscribe()

	s.publishSessionCatalogEvent(SessionCatalogEvent{Type: "session_created", SessionID: "sess-1", ProjectID: "proj-1"})

	select {
	case event := <-events:
		if event.Type != "session_created" || event.SessionID != "sess-1" || event.ProjectID != "proj-1" {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for catalog event")
	}
}

func TestSessionCatalogSubscriptionFiltersByProject(t *testing.T) {
	s := &Server{}
	events, unsubscribe := s.SubscribeSessionCatalogEvents("proj-1")
	defer unsubscribe()

	s.publishSessionCatalogEvent(SessionCatalogEvent{Type: "session_created", SessionID: "other", ProjectID: "proj-2"})
	s.publishSessionCatalogEvent(SessionCatalogEvent{Type: "heartbeat"})
	s.publishSessionCatalogEvent(SessionCatalogEvent{Type: "session_created", SessionID: "wanted", ProjectID: "proj-1"})

	select {
	case event := <-events:
		if event.SessionID != "wanted" {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for project-filtered catalog event")
	}
}

func TestHandleCreateSessionPublishesCatalogEvent(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	now := time.Now()
	if err := store.SaveProject(&storage.Project{ID: "proj-1", Name: "Demo", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	events, unsubscribe := server.SubscribeSessionCatalogEvents("proj-1")
	defer unsubscribe()

	body, err := json.Marshal(CreateSessionRequest{AgentID: "build", ProjectID: "proj-1", Task: "from chrome"})
	if err != nil {
		t.Fatalf("failed to encode request: %v", err)
	}
	req := httptest.NewRequest(stdhttp.MethodPost, "/sessions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateSession(rec, req)
	if rec.Code != stdhttp.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var created CreateSessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}

	select {
	case event := <-events:
		if event.Type != "session_created" {
			t.Fatalf("type = %q, want session_created", event.Type)
		}
		if event.SessionID != created.ID {
			t.Fatalf("session_id = %q, want %q", event.SessionID, created.ID)
		}
		if event.ProjectID != "proj-1" {
			t.Fatalf("project_id = %q, want proj-1", event.ProjectID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session_created catalog event")
	}
}

func TestHandleDeleteSessionPublishesCatalogEvent(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	now := time.Now()
	if err := store.SaveProject(&storage.Project{ID: "proj-1", Name: "Demo", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	projectID := "proj-1"
	sess.ProjectID = &projectID
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	events, unsubscribe := server.SubscribeSessionCatalogEvents("proj-1")
	defer unsubscribe()

	req := httptest.NewRequest(stdhttp.MethodDelete, "/sessions/"+sess.ID, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("sessionID", sess.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	server.handleDeleteSession(rec, req)
	if rec.Code != stdhttp.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	select {
	case event := <-events:
		if event.Type != "session_deleted" || event.SessionID != sess.ID || event.ProjectID != "proj-1" {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session_deleted catalog event")
	}
}

func TestHandleSessionCatalogEventsStreamsPublishedEvents(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, stdhttp.MethodGet, "/sessions/events?project_id=proj-1", nil)

	recorder := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		server.handleSessionCatalogEvents(recorder, req)
		close(done)
	}()

	if !recorder.waitFor("event: catalog_ready", time.Second) {
		t.Fatalf("timed out waiting for catalog_ready, body: %s", recorder.String())
	}
	if got := recorder.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}

	server.publishSessionCatalogEvent(SessionCatalogEvent{Type: "session_created", SessionID: "sess-1", ProjectID: "proj-1"})
	if !recorder.waitFor(`"session_id":"sess-1"`, time.Second) {
		t.Fatalf("timed out waiting for catalog event, body: %s", recorder.String())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not stop after request cancellation")
	}
}

func TestSessionCatalogEventsRouteIsNotTreatedAsSessionID(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, stdhttp.MethodGet, "/sessions/events", nil)
	recorder := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		server.router.ServeHTTP(recorder, req)
		close(done)
	}()

	if !recorder.waitFor("event: catalog_ready", time.Second) {
		t.Fatalf("GET /sessions/events did not stream catalog_ready, body: %s", recorder.String())
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Content-Type = %q, want event-stream", recorder.Header().Get("Content-Type"))
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not stop after request cancellation")
	}
}
