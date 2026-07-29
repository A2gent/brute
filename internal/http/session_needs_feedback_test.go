package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/go-chi/chi/v5"
)

func TestUpdateSessionNeedsFeedback(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)

	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	body, err := json.Marshal(UpdateSessionNeedsFeedbackRequest{NeedsFeedback: true})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/sessions/"+sess.ID+"/needs-feedback", bytes.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("sessionID", sess.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()

	server.handleUpdateSessionNeedsFeedback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp SessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !sessionNeedsFeedbackFromMetadata(resp.Metadata) {
		t.Fatalf("expected needs_feedback metadata on response: %#v", resp.Metadata)
	}

	fresh, err := sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if !sessionNeedsFeedback(fresh) {
		t.Fatalf("expected persisted needs_feedback flag")
	}

	clearBody, err := json.Marshal(UpdateSessionNeedsFeedbackRequest{NeedsFeedback: false})
	if err != nil {
		t.Fatalf("encode clear request: %v", err)
	}
	clearReq := httptest.NewRequest(http.MethodPut, "/sessions/"+sess.ID+"/needs-feedback", bytes.NewReader(clearBody))
	clearRouteCtx := chi.NewRouteContext()
	clearRouteCtx.URLParams.Add("sessionID", sess.ID)
	clearReq = clearReq.WithContext(context.WithValue(clearReq.Context(), chi.RouteCtxKey, clearRouteCtx))
	clearRec := httptest.NewRecorder()
	server.handleUpdateSessionNeedsFeedback(clearRec, clearReq)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body = %s", clearRec.Code, clearRec.Body.String())
	}

	fresh, err = sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session after clear: %v", err)
	}
	if sessionNeedsFeedback(fresh) {
		t.Fatalf("expected needs_feedback flag to be cleared")
	}
}

func TestSessionNeedsFeedbackHelpers(t *testing.T) {
	sess := session.New("build")
	if sessionNeedsFeedback(sess) {
		t.Fatalf("expected false by default")
	}
	setSessionNeedsFeedback(sess, true)
	if !sessionNeedsFeedback(sess) {
		t.Fatalf("expected true after set")
	}
	setSessionNeedsFeedback(sess, false)
	if sessionNeedsFeedback(sess) {
		t.Fatalf("expected false after clear")
	}
}
