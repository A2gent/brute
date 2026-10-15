package http

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func readCatalogEvent(t *testing.T, events <-chan SessionCatalogEvent) SessionCatalogEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("catalog subscription closed")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for catalog event")
	}
	return SessionCatalogEvent{}
}

func publishNumbered(hub *sessionCatalogHub, count int) []SessionCatalogEvent {
	for i := 0; i < count; i++ {
		hub.publish(SessionCatalogEvent{Type: "session_updated", SessionID: "sess-" + strconv.Itoa(i)})
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return append([]SessionCatalogEvent(nil), hub.ring...)
}

func TestSessionCatalogReplaysFromLastEventID(t *testing.T) {
	hub := newSessionCatalogHub()
	published := publishNumbered(hub, 3)

	sub := hub.subscribe("", published[0].ID, true)
	defer hub.unsubscribe(sub)

	for _, want := range published[1:] {
		got := readCatalogEvent(t, sub.events)
		if got.ID != want.ID || got.SessionID != want.SessionID {
			t.Fatalf("replayed %+v, want %+v", got, want)
		}
	}
	ready := readCatalogEvent(t, sub.events)
	if ready.Type != "catalog_ready" || ready.ID != published[2].ID {
		t.Fatalf("after replay got %+v, want catalog_ready at %s", ready, published[2].ID)
	}

	// Live events continue after the replay with the next sequence number.
	hub.publish(SessionCatalogEvent{Type: "session_updated", SessionID: "live"})
	if live := readCatalogEvent(t, sub.events); live.SessionID != "live" || live.Seq != 4 {
		t.Fatalf("live event = %+v, want seq 4", live)
	}
}

func TestSessionCatalogResyncWhenCursorTooOldOrForeign(t *testing.T) {
	hub := newSessionCatalogHub()
	published := publishNumbered(hub, sessionCatalogRingSize+5)

	tooOld := hub.epoch + "-1"
	sub := hub.subscribe("", tooOld, true)
	if got := readCatalogEvent(t, sub.events); got.Type != "resync" || got.Full || got.ID != published[len(published)-1].ID {
		t.Fatalf("too-old cursor got %+v, want partial resync at latest cursor", got)
	}
	hub.unsubscribe(sub)

	// A cursor from a previous server run cannot be served by updated_since tombstones.
	sub = hub.subscribe("", "otherepoch-3", true)
	if got := readCatalogEvent(t, sub.events); got.Type != "resync" || !got.Full {
		t.Fatalf("foreign cursor got %+v, want full resync", got)
	}
	hub.unsubscribe(sub)
}

func TestSessionCatalogSlowSubscriberLosesNoEvents(t *testing.T) {
	hub := newSessionCatalogHub()
	sub := hub.subscribe("", "", false)
	defer hub.unsubscribe(sub)

	// Far more than the 128-slot channel; the old select/default dropped these.
	const total = 1000
	publishNumbered(hub, total)

	for want := uint64(1); want <= total; want++ {
		if got := readCatalogEvent(t, sub.events); got.Seq != want {
			t.Fatalf("seq = %d, want %d (type %s)", got.Seq, want, got.Type)
		}
	}
}

func TestSessionCatalogBacklogOverflowForcesResync(t *testing.T) {
	hub := newSessionCatalogHub()
	sub := hub.subscribe("", "", false)
	defer hub.unsubscribe(sub)

	total := sessionCatalogMaxBacklog + 500
	publishNumbered(hub, total)

	sawResync := false
	var last SessionCatalogEvent
	for last.Seq != uint64(total) {
		last = readCatalogEvent(t, sub.events)
		if last.Type == "resync" {
			sawResync = true
		}
	}
	if !sawResync {
		t.Fatal("overflowed subscriber was not told to resync")
	}
}

func TestSessionManagerSavesPublishFullRows(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := server.SubscribeSessionCatalogEvents("")
	defer unsubscribe()

	// Token totals and task progress used to change without any catalog event.
	sess.Metadata = map[string]interface{}{"total_input_tokens": 7, "total_output_tokens": 5}
	sess.TaskProgress = "- [x] one"
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	got := readCatalogEvent(t, events)
	if got.Type != "session_updated" || got.Session == nil || got.Session.TotalTokens != 12 || got.Session.TaskProgress != "- [x] one" {
		t.Fatalf("save event = %+v, row = %+v", got, got.Session)
	}

	// Saving unchanged list fields must not wake subscribers.
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	if err := server.sessionManager.SetSessionSummary(sess.ID, "short summary"); err != nil {
		t.Fatal(err)
	}
	if got := readCatalogEvent(t, events); got.Session == nil || got.Session.Summary != "short summary" {
		t.Fatalf("summary event = %+v", got)
	}
}

func TestListSessionsUpdatedSinceReturnsChangesAndTombstones(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	kept, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	removed, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	since := time.Now().UTC()
	time.Sleep(5 * time.Millisecond)
	if err := server.sessionManager.SetSessionSummary(kept.ID, "changed"); err != nil {
		t.Fatal(err)
	}
	if err := server.sessionManager.Delete(removed.ID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions?archived=include&updated_since="+url.QueryEscape(since.Format(time.RFC3339Nano)), nil)
	rec := httptest.NewRecorder()
	server.handleListSessions(rec, req)
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var items []SessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	byID := map[string]SessionListItem{}
	for _, item := range items {
		byID[item.ID] = item
	}
	if len(items) != 2 || byID[kept.ID].Summary != "changed" || !byID[removed.ID].Deleted {
		t.Fatalf("updated_since items = %+v", items)
	}
}

func TestSessionCatalogStreamWritesIDsAndHonorsLastEventID(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	hub := server.catalogHub()
	published := publishNumbered(hub, 2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, stdhttp.MethodGet, "/sessions/events", nil)
	req.Header.Set("Last-Event-ID", published[0].ID)
	recorder := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		server.router.ServeHTTP(recorder, req)
		close(done)
	}()

	if !recorder.waitFor("event: catalog_ready", time.Second) {
		t.Fatalf("no catalog_ready, body: %s", recorder.String())
	}
	body := recorder.String()
	if strings.Contains(body, `"session_id":"sess-0"`) || !strings.Contains(body, "id: "+published[1].ID+"\nevent: session_updated") {
		t.Fatalf("stream did not replay only events after Last-Event-ID:\n%s", body)
	}
	if recorder.Header().Get("Content-Encoding") != "" {
		t.Fatal("event stream must not be compressed")
	}
	cancel()
	<-done
}

func TestListSessionsResponseIsGzipped(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	if _, err := server.sessionManager.Create("build"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	reader, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var items []SessionListItem
	if err := json.Unmarshal(raw, &items); err != nil || len(items) != 1 {
		t.Fatalf("decoded %d items, err %v", len(items), err)
	}
}
