package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/go-chi/chi/v5"
)

type flushRecorder struct {
	header http.Header
	mu     sync.Mutex
	body   bytes.Buffer
	cond   *sync.Cond
}

func newFlushRecorder() *flushRecorder {
	r := &flushRecorder{header: http.Header{}}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *flushRecorder) Header() http.Header { return r.header }

func (r *flushRecorder) WriteHeader(_ int) {}

func (r *flushRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, err := r.body.Write(p)
	r.cond.Broadcast()
	return n, err
}

func (r *flushRecorder) Flush() {}

func (r *flushRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func (r *flushRecorder) waitFor(needle string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	r.mu.Lock()
	defer r.mu.Unlock()
	for !strings.Contains(r.body.String(), needle) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		timer := time.AfterFunc(remaining, func() {
			r.mu.Lock()
			r.cond.Broadcast()
			r.mu.Unlock()
		})
		r.cond.Wait()
		timer.Stop()
	}
	return true
}

func TestSessionEventSubscriptionReceivesPublishedEvents(t *testing.T) {
	s := &Server{sessionEventSubs: make(map[string]map[chan ChatStreamEvent]struct{})}
	events, unsubscribe := s.SubscribeSessionEvents("session-1")
	defer unsubscribe()

	s.publishSessionEvent("session-1", ChatStreamEvent{Type: "assistant_delta", Delta: "hello"})

	select {
	case event := <-events:
		if event.Type != "assistant_delta" || event.Delta != "hello" {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
	}
}

func TestSessionEventSubscriptionIgnoresOtherSessionsAndHeartbeats(t *testing.T) {
	s := &Server{sessionEventSubs: make(map[string]map[chan ChatStreamEvent]struct{})}
	events, unsubscribe := s.SubscribeSessionEvents("session-1")
	defer unsubscribe()

	s.publishSessionEvent("session-2", ChatStreamEvent{Type: "assistant_delta", Delta: "wrong"})
	s.publishSessionEvent("session-1", ChatStreamEvent{Type: "heartbeat"})

	select {
	case event := <-events:
		t.Fatalf("unexpected event: %#v", event)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestHandleSessionEventsReplaysSnapshotAndPublishedEvents(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager(t.TempDir()), sessionManager, store, speechcache.New(0), 0)
	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("hello")
	sess.SetStatus(session.StatusRunning)
	if err := sessionManager.Save(sess); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/sessions/"+sess.ID+"/events", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("sessionID", sess.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	recorder := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		server.handleSessionEvents(recorder, req)
		close(done)
	}()

	if !recorder.waitFor("event: session_snapshot", time.Second) {
		t.Fatalf("timed out waiting for snapshot, body: %s", recorder.String())
	}
	if got := recorder.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	server.publishSessionEvent(sess.ID, ChatStreamEvent{Type: "assistant_delta", Delta: "Hi"})
	if !recorder.waitFor(`"delta":"Hi"`, time.Second) {
		t.Fatalf("timed out waiting for assistant delta, body: %s", recorder.String())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not stop after request cancellation")
	}
}
