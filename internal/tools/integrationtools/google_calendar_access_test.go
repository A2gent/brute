package integrationtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestAllowedCalendarIDs(t *testing.T) {
	in := &storage.Integration{Config: map[string]string{"allowed_calendars": " a@x.com,\nfamily@group.calendar.google.com ,, "}}
	got := AllowedCalendarIDs(in)
	want := []string{"a@x.com", "family@group.calendar.google.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if !calendarAllowed(got, "a@x.com") || calendarAllowed(got, "other") {
		t.Fatal("allow-list check wrong")
	}
	if !calendarAllowed(nil, "anything") {
		t.Fatal("empty list must allow all (legacy integrations)")
	}
}

func newCalendarWriteFixture(t *testing.T, cfg map[string]string, handler http.HandlerFunc) *storage.SQLiteStore {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := googleCalendarEventsBase
	googleCalendarEventsBase = srv.URL
	t.Cleanup(func() { googleCalendarEventsBase = old })
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]string{
		"client_id": "id", "client_secret": "s", "refresh_token": "rt", "access_token": "tok",
		"token_expiry": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	for k, v := range cfg {
		base[k] = v
	}
	if err := store.SaveIntegration(&storage.Integration{ID: "c1", Provider: GoogleCalendarProvider, Name: "Cal", Mode: "notify_only", Enabled: true, Config: base, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestCalendarCreateEventRefusals(t *testing.T) {
	noCall := func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected API call %s", r.URL.Path) }
	args := json.RawMessage(`{"summary":"Lunch","start":"2026-10-14T12:00:00+02:00"}`)

	store := newCalendarWriteFixture(t, map[string]string{"access_level": "read", "allowed_calendars": "a", "writable_calendars": "a"}, noCall)
	if res, _ := NewGoogleCalendarCreateEventTool(store).Execute(context.Background(), args); res.Success || !strings.Contains(res.Error, "read-only") {
		t.Fatalf("read-only must refuse: %+v", res)
	}
	store = newCalendarWriteFixture(t, map[string]string{"access_level": "write", "allowed_calendars": "a,b", "writable_calendars": "a"}, noCall)
	res, _ := NewGoogleCalendarCreateEventTool(store).Execute(context.Background(), json.RawMessage(`{"summary":"x","start":"2026-10-14T12:00:00Z","calendar_id":"b"}`))
	if res.Success || !strings.Contains(res.Error, "not writable") {
		t.Fatalf("non-writable calendar must refuse: %+v", res)
	}
}

func TestCalendarCreateEventPosts(t *testing.T) {
	var path, query string
	var body map[string]interface{}
	store := newCalendarWriteFixture(t, map[string]string{"access_level": "write", "allowed_calendars": "a@x.com", "writable_calendars": "a@x.com"},
		func(w http.ResponseWriter, r *http.Request) {
			path, query = r.URL.Path, r.URL.RawQuery
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write([]byte(`{"id":"e1","htmlLink":"https://cal/e1"}`))
		})
	res, _ := NewGoogleCalendarCreateEventTool(store).Execute(context.Background(), json.RawMessage(`{"summary":"Lunch","start":"2026-10-14T12:00:00+02:00"}`))
	if !res.Success {
		t.Fatalf("create: %+v", res)
	}
	if path != "/a@x.com/events" || query != "sendUpdates=none" {
		t.Fatalf("path=%q query=%q", path, query)
	}
	end := body["end"].(map[string]interface{})["dateTime"]
	if end != "2026-10-14T13:00:00+02:00" {
		t.Fatalf("default end = %v", end)
	}
}

func TestCalendarScopesByLevel(t *testing.T) {
	if s := strings.Join(GoogleCalendarScopes("read"), " "); strings.Contains(s, "calendar.events") {
		t.Fatalf("read must not request events scope: %s", s)
	}
	if s := strings.Join(GoogleCalendarScopes("write"), " "); !strings.Contains(s, "calendar.events") {
		t.Fatalf("write must request events scope: %s", s)
	}
}
