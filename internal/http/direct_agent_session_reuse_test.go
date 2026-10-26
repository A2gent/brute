package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

// fakeChildAgent serves the subset of the Brute API a parent uses for a direct
// agent session turn.
type fakeChildAgent struct {
	mu        sync.Mutex
	status    string
	pending   bool
	source    string
	decisions []string
}

func (f *fakeChildAgent) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/child-1":
			metadata := map[string]interface{}{"source": f.source}
			if f.pending {
				metadata[session.TransitionMetadataKey] = []map[string]interface{}{{"id": "tr-1", "confirmation": "pending", "phase": "awaiting_confirmation"}}
			}
			_ = json.NewEncoder(w).Encode(SessionResponse{ID: "child-1", Status: f.status, Metadata: metadata})
		case r.Method == http.MethodPost && r.URL.Path == "/sessions/child-1/provider-transitions/tr-1/decision":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.decisions = append(f.decisions, body["decision"])
			f.pending = false
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestReusableDirectAgentChild(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	cases := []struct {
		name          string
		status        string
		pending       bool
		source        string
		agentName     string
		storedChild   string
		want          string
		wantDecisions []string
	}{
		{name: "reuses previous child keeping its route", status: "completed", agentName: "qa", storedChild: "child-1", want: "child-1"},
		{name: "delegated child is not reused", status: "completed", source: "delegate_to_agent", agentName: "qa", storedChild: "child-1", want: ""},
		{name: "new message rejects pending switch", status: "input_required", pending: true, agentName: "qa", storedChild: "child-1", want: "child-1", wantDecisions: []string{"reject"}},
		{name: "running child is not reused", status: "running", agentName: "qa", storedChild: "child-1", want: ""},
		{name: "other agent is not reused", status: "completed", agentName: "other", storedChild: "child-1", want: ""},
		{name: "missing child creates new", status: "completed", agentName: "qa", storedChild: "gone", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.source
			if source == "" {
				source = "direct_agent_session"
			}
			fake := &fakeChildAgent{status: tc.status, pending: tc.pending, source: source}
			child := httptest.NewServer(fake.handler())
			defer child.Close()
			sess := &session.Session{ID: "parent"}
			addDockerChildSessionEntry(sess, &LocalDockerAgent{Name: "qa", APIURL: child.URL}, tc.storedChild, nil)
			got := server.reusableDirectAgentChild(context.Background(), sess, &LocalDockerAgent{Name: tc.agentName, APIURL: child.URL})
			if got != tc.want {
				t.Fatalf("child = %q, want %q", got, tc.want)
			}
			if strings.Join(fake.decisions, ",") != strings.Join(tc.wantDecisions, ",") {
				t.Fatalf("decisions = %v, want %v", fake.decisions, tc.wantDecisions)
			}
		})
	}
}
