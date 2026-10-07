package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestSessionEventsOmitOnlyMatchingTranscriptVersion(t *testing.T) {
	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("history payload")
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{sess.UpdatedAt.Format(time.RFC3339Nano), "older", ""} {
		t.Run(version, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/sessions/"+sess.ID+"/events?after_updated_at="+url.QueryEscape(version), nil)
			route := chi.NewRouteContext()
			route.URLParams.Add("sessionID", sess.ID)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
			recorder := newFlushRecorder()
			done := make(chan struct{})
			go func() { server.handleSessionEvents(recorder, req); close(done) }()
			if !recorder.waitFor("event: session_snapshot", time.Second) {
				t.Fatal("missing handshake")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("stream failed to close")
			}
			hasHistory := strings.Contains(recorder.String(), "history payload")
			if hasHistory == (version == sess.UpdatedAt.Format(time.RFC3339Nano)) {
				t.Fatalf("unexpected snapshot: %s", recorder.String())
			}
		})
	}
}

func TestSessionListRelationFilter(t *testing.T) {
	server, _ := newBruteHTTPProxyTestServer(t)
	parent, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	current, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	current.ParentID = &parent.ID
	if err := server.sessionManager.Save(current); err != nil {
		t.Fatal(err)
	}
	child, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	child.ParentID = &current.ID
	if err := server.sessionManager.Save(child); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/sessions/?related_to="+current.ID+"&archived=include", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("response: %s", rec.Body.String())
	}
	var rows []SessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected parent and child, got %v", rows)
	}
	for _, row := range rows {
		if row.ID != parent.ID && row.ID != child.ID {
			t.Fatalf("unrelated session: %s", row.ID)
		}
	}
}
