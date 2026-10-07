package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestGetSessionHistoryPaginationAndValidation(t *testing.T) {
	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		sess.AddUserMessage(fmt.Sprintf("message-%d", i))
	}
	sess.SetStatus(session.StatusCompleted)
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	fetch := func(query string) (int, SessionResponse) {
		t.Helper()
		rec := httptest.NewRecorder()
		server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sessions/"+sess.ID+query, nil))
		var result SessionResponse
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, result
	}
	code, page := fetch("?message_limit=3")
	if code != http.StatusOK || len(page.Messages) != 3 || page.MessagePage == nil || !page.MessagePage.HasMore {
		t.Fatalf("bad page: %d %+v", code, page)
	}
	if page.Messages[0].Content != "message-6" {
		t.Fatalf("wrong tail: %v", page.Messages)
	}
	code, older := fetch("?message_limit=3&before_message=" + page.MessagePage.BeforeMessage)
	if code != http.StatusOK || older.Messages[0].Content != "message-3" {
		t.Fatalf("wrong older page: %d %+v", code, older)
	}
	for _, query := range []string{"?message_limit=0", "?message_limit=-1", "?message_limit=501", "?message_limit=abc"} {
		if code, _ := fetch(query); code != http.StatusBadRequest {
			t.Fatalf("invalid limit accepted: %s %d", query, code)
		}
	}
	if code, _ := fetch("?message_limit=3&before_message=missing"); code != http.StatusConflict {
		t.Fatalf("missing cursor accepted: %d", code)
	}
	code, full := fetch("")
	if code != http.StatusOK || len(full.Messages) != 9 || full.MessagePage != nil {
		t.Fatal("legacy full transcript API changed")
	}
	sess.SetStatus(session.StatusRunning)
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	code, full = fetch("?message_limit=3")
	if code != http.StatusOK || len(full.Messages) != 9 || full.MessagePage.HasMore {
		t.Fatal("active history truncated")
	}
}
