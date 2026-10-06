package http

import (
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

func newSessionArchiveTestServer(t *testing.T) (*Server, *session.Manager) {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()

	sessionManager := session.NewManager(store)
	return NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0), sessionManager
}

func callSessionArchiveHandler(t *testing.T, handler http.HandlerFunc, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sessionID+"/archive", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("sessionID", sessionID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func listSessionIDs(t *testing.T, server *Server, query string) map[string]SessionListItem {
	t.Helper()
	rec := httptest.NewRecorder()
	server.handleListSessions(rec, httptest.NewRequest(http.MethodGet, "/sessions"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var items []SessionListItem
	if err := json.NewDecoder(rec.Body).Decode(&items); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	out := make(map[string]SessionListItem, len(items))
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func TestArchiveSessionHidesItAndDescendantsFromDefaultList(t *testing.T) {
	server, sessionManager := newSessionArchiveTestServer(t)

	parent, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	parent.SetStatus(session.StatusCompleted)
	if err := sessionManager.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	child, err := sessionManager.CreateWithParent("build", parent.ID)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	child.SetStatus(session.StatusCompleted)
	if err := sessionManager.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}
	other, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}

	rec := callSessionArchiveHandler(t, server.handleArchiveSession, parent.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp SessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode archive response: %v", err)
	}
	if resp.ArchivedAt == nil {
		t.Fatalf("expected archived_at on response")
	}

	active := listSessionIDs(t, server, "")
	if _, ok := active[parent.ID]; ok {
		t.Fatalf("archived parent must be hidden from default list")
	}
	if _, ok := active[child.ID]; ok {
		t.Fatalf("archived child must be hidden from default list")
	}
	if _, ok := active[other.ID]; !ok {
		t.Fatalf("non-archived session must stay in default list")
	}

	archived := listSessionIDs(t, server, "?archived=only")
	if len(archived) != 2 || archived[parent.ID].ArchivedAt == nil {
		t.Fatalf("archived=only should return parent and child with archived_at, got %#v", archived)
	}
	if all := listSessionIDs(t, server, "?archived=include"); len(all) != 3 {
		t.Fatalf("archived=include should return all sessions, got %d", len(all))
	}

	// Restoring clears the analysis marker so new work in the session gets reviewed again.
	reloaded, err := sessionManager.Get(parent.ID)
	if err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	reloaded.Metadata[storage.SessionArchiveAnalyzedAtKey] = "2026-01-01T00:00:00Z"
	if err := sessionManager.Save(reloaded); err != nil {
		t.Fatalf("save analyzed marker: %v", err)
	}
	if rec := callSessionArchiveHandler(t, server.handleUnarchiveSession, parent.ID); rec.Code != http.StatusOK {
		t.Fatalf("unarchive status = %d, body = %s", rec.Code, rec.Body.String())
	}
	restored, err := sessionManager.Get(parent.ID)
	if err != nil {
		t.Fatalf("reload restored: %v", err)
	}
	if _, ok := restored.Metadata[storage.SessionArchivedAtKey]; ok {
		t.Fatalf("expected archived_at cleared")
	}
	if _, ok := restored.Metadata[storage.SessionArchiveAnalyzedAtKey]; ok {
		t.Fatalf("expected archive_analyzed_at cleared")
	}
	if _, ok := listSessionIDs(t, server, "")[child.ID]; !ok {
		t.Fatalf("restoring parent should restore descendants too")
	}
}

func TestArchiveSessionRejectsRunningSession(t *testing.T) {
	server, sessionManager := newSessionArchiveTestServer(t)
	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// WHY: a running agent loop saves its in-memory metadata and would silently drop archived_at.
	rec := callSessionArchiveHandler(t, server.handleArchiveSession, sess.ID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}
