package http

import (
	"testing"
	"time"
)

// A burst of saves from a running session must produce one immediate event plus
// one trailing event, not one event per save.
func TestSessionCatalogUpdatesAreThrottled(t *testing.T) {
	server, sess := newCatalogStartTestServer(t, "http://127.0.0.1:0", "")
	events, cancel := server.SubscribeSessionCatalogEvents("")
	defer cancel()

	for i := 0; i < 20; i++ {
		sess.Title = "t" + string(rune('a'+i))
		if err := server.sessionManager.Save(sess); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(catalogUpdateThrottle + 300*time.Millisecond)

	updates := 0
	for drained := false; !drained; {
		select {
		case event := <-events:
			if event.Type == "session_updated" && event.SessionID == sess.ID {
				updates++
			}
		default:
			drained = true
		}
	}
	if updates < 1 || updates > 3 {
		t.Fatalf("session_updated events for 20 saves = %d, want 1-3", updates)
	}
}
