package http

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestProviderTransitionHTTPConsentAndIdempotency(t *testing.T) {
	s, _ := newBruteHTTPProxyTestServer(t)
	sess, _ := s.sessionManager.Create("test")
	sess.Metadata = map[string]interface{}{"provider": "lmstudio", "model": "source"}
	_ = s.sessionManager.Save(sess)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRecorder()
		s.router.ServeHTTP(r, httptest.NewRequest(method, path, strings.NewReader(body)))
		return r
	}
	path := "/sessions/" + sess.ID + "/provider-transitions"
	body := `{"id":"test-transition","provider":"lmstudio","model":"target","mode":"confirm"}`
	for _, want := range []int{202, 200} {
		rec := call("POST", path, body)
		if rec.Code != want {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	fresh, _ := s.sessionManager.Get(sess.ID)
	tr := session.CurrentProviderTransition(fresh)
	if fresh.Metadata["provider"] != "lmstudio" || fresh.Metadata["model"] != "source" || tr.Confirmation != "pending" {
		t.Fatal("changed before consent")
	}
	rec := call("POST", path, strings.Replace(body, `"target"`, `"different"`, 1))
	if rec.Code != 409 {
		t.Fatalf("reused id %d", rec.Code)
	}
	decision := path + "/test-transition/decision"
	for i := 0; i < 2; i++ {
		rec = call("POST", decision, `{"decision":"reject"}`)
		if rec.Code != 200 {
			t.Fatalf("reject %d %s", rec.Code, rec.Body)
		}
	}
	if rec = call("POST", decision, `{"decision":"approve"}`); rec.Code != 409 {
		t.Fatal("reversed decision")
	}
	rec = call("GET", path, "")
	var out struct {
		Transitions []session.ProviderTransition `json:"transitions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Transitions) != 1 || out.Transitions[0].Phase != "rejected" {
		t.Fatalf("durability %s", rec.Body)
	}
}
func TestProviderTransitionHTTPActiveRunAndChatGuard(t *testing.T) {
	s, _ := newBruteHTTPProxyTestServer(t)
	sess, _ := s.sessionManager.Create("test")
	_, release, err := s.sessionManager.AcquireRun(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("POST", "/sessions/"+sess.ID+"/provider-transitions", strings.NewReader(`{"id":"one","provider":"lmstudio","mode":"confirm"}`)))
	if rec.Code != 409 {
		t.Fatalf("active %d", rec.Code)
	}
	release()
	tr, _ := session.NewProviderTransition("one", session.ProviderEndpoint{}, session.ProviderEndpoint{Provider: "lmstudio"}, "manual", "confirm")
	session.PutProviderTransition(sess, tr)
	_ = s.sessionManager.Save(sess)
	rec = httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("POST", "/sessions/"+sess.ID+"/chat", strings.NewReader(`{"message":"bypass"}`)))
	if rec.Code != 409 {
		t.Fatalf("chat bypass %d %s", rec.Code, rec.Body)
	}
	fresh, _ := s.sessionManager.Get(sess.ID)
	if len(fresh.Messages) != 0 {
		t.Fatal("guard mutated history")
	}
}

func TestRecoverInterruptedRunsPausesRunningSessions(t *testing.T) {
	s, _ := newBruteHTTPProxyTestServer(t)
	sess, _ := s.sessionManager.Create("test")
	sess.SetStatus(session.StatusRunning)
	_ = s.sessionManager.Save(sess)
	s.recoverInterruptedRuns()
	fresh, _ := s.sessionManager.Get(sess.ID)
	if fresh.Status != session.StatusPaused {
		t.Fatalf("status %s", fresh.Status)
	}
}
