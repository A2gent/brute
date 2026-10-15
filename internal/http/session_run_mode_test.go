package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func runModeTestServer(t *testing.T, status int, body string) (*Server, *atomic.Int32, <-chan jev.SystemOneRequest) {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	calls := &atomic.Int32{}
	requests := make(chan jev.SystemOneRequest, 10)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request jev.SystemOneRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests <- request
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(fake.Close)
	cfg := config.DefaultConfig()
	cfg.WorkDir = t.TempDir()
	cfg.Providers["jev"] = config.Provider{APIKey: "jev-test-credential", BaseURL: fake.URL}
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Server{config: cfg, store: store, sessionManager: session.NewManager(store)}, calls, requests
}

func TestAutoSessionRunMode(t *testing.T) {
	for _, tc := range []struct {
		name, body, label string
		status            int
		now               bool
	}{
		{"read only", `{"answers":{"run_mode":{"type":"choice","choice":"read_only","confidence":0.99}}}`, "read_only", 200, true},
		{"threshold", `{"answers":{"run_mode":{"type":"choice","choice":"read_only","confidence":0.85}}}`, "read_only", 200, true},
		{"low confidence", `{"answers":{"run_mode":{"type":"choice","choice":"read_only","confidence":0.84}}}`, "read_only", 200, false},
		{"modifies", `{"answers":{"run_mode":{"type":"choice","choice":"modifies_project","confidence":0.99}}}`, "modifies_project", 200, false},
		{"external", `{"answers":{"run_mode":{"type":"choice","choice":"external_side_effects","confidence":0.99}}}`, "external_side_effects", 200, false},
		{"error", `{}`, "unknown", 500, false},
		{"missing", `{"answers":{}}`, "unknown", 200, false},
		{"invalid label", `{"answers":{"run_mode":{"type":"choice","choice":"now","confidence":1}}}`, "unknown", 200, false},
		{"invalid confidence", `{"answers":{"run_mode":{"type":"choice","choice":"read_only","confidence":1.1}}}`, "unknown", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, calls, requests := runModeTestServer(t, tc.status, tc.body)
			req := &CreateSessionRequest{Task: "Analyze the queue", QueueMode: "auto"}
			sess := session.New("test")
			s.applySessionRunMode(context.Background(), req, sess)
			if req.Queued == tc.now || sess.IsAutoReadOnly() != tc.now {
				t.Fatalf("queued=%v readOnly=%v", req.Queued, sess.IsAutoReadOnly())
			}
			wantMode, wantStatus := "serial", session.StatusQueued
			if tc.now {
				wantMode, wantStatus = "now", session.StatusRunning
			}
			if req.QueueMode != wantMode || sess.Status != wantStatus {
				t.Fatalf("mode=%s status=%s", req.QueueMode, sess.Status)
			}
			decision := sess.RunModeDecision()
			if decision == nil || decision.Source != "jev" || decision.Label != tc.label {
				t.Fatalf("decision=%+v", decision)
			}
			if calls.Load() != 1 || sess.Metadata["run_mode_attempt_count"] != float64(1) {
				t.Fatalf("calls=%d metadata=%+v", calls.Load(), sess.Metadata)
			}
			request := <-requests
			question := request.Questions["run_mode"]
			criteria, _ := question.Criteria.(map[string]interface{})
			if question.Type != "choice" || len(request.Questions) != 1 || len(criteria) != 3 {
				t.Fatalf("request=%+v", request)
			}
		})
	}
}

func TestManualRunModeOverridesAuto(t *testing.T) {
	for _, tc := range []struct {
		task, mode, want string
		queued           bool
	}{
		{"/q Analyze this", "auto", "serial", false},
		{"/queue: Analyze this", "auto", "serial", false},
		{"/now Edit this", "auto", "now", true},
		{"Analyze this", "serial", "serial", false},
		{"Edit this", "now", "now", true},
		{"Analyze this", "auto", "serial", true},
	} {
		t.Run(tc.task+tc.mode, func(t *testing.T) {
			s, calls, _ := runModeTestServer(t, 500, `{}`)
			req := &CreateSessionRequest{Task: tc.task, QueueMode: tc.mode, Queued: tc.queued}
			applyLeadingSessionQueueDirective(req)
			sess := session.New("test")
			s.applySessionRunMode(context.Background(), req, sess)
			if req.QueueMode != tc.want || req.Queued != (tc.want == "serial") || calls.Load() != 0 || sess.RunModeDecision().Source != "manual" {
				t.Fatalf("req=%+v decision=%+v calls=%d", req, sess.RunModeDecision(), calls.Load())
			}
		})
	}
}

func TestAutoRunModeEgressAndUnavailable(t *testing.T) {
	for _, task := range []string{"", "Read this \xff", "Search " + strings.Repeat("context ", 6000) + "jev-test-credential" + strings.Repeat("context ", 6000)} {
		s, calls, _ := runModeTestServer(t, 200, `{}`)
		req := &CreateSessionRequest{Task: task, QueueMode: "auto"}
		s.applySessionRunMode(context.Background(), req, session.New("test"))
		if !req.Queued || calls.Load() != 0 {
			t.Fatalf("unsafe prompt classified: queued=%v calls=%d", req.Queued, calls.Load())
		}
	}
	s, calls, _ := runModeTestServer(t, 200, `{}`)
	delete(s.config.Providers, "jev")
	req := &CreateSessionRequest{Task: "Analyze this", QueueMode: "auto"}
	s.applySessionRunMode(context.Background(), req, session.New("test"))
	if !req.Queued || calls.Load() != 0 {
		t.Fatal("unavailable Jev did not fail safe")
	}
}

func TestAutoRunModeHeadTailAndAPI(t *testing.T) {
	s, calls, requests := runModeTestServer(t, 200, `{"answers":{"run_mode":{"type":"choice","choice":"read_only","confidence":0.99}}}`)
	s.toolManager = tools.NewManager(s.config.WorkDir)
	task := "Analyze " + strings.Repeat("上下文", 10000) + " final question"
	body, _ := json.Marshal(CreateSessionRequest{Task: task, QueueMode: "auto", Provider: "openai"})
	w := httptest.NewRecorder()
	s.handleCreateSession(w, httptest.NewRequest("POST", "/sessions", bytes.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var created CreateSessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != "running" || created.RunModeDecision == nil || created.RunModeDecision.Label != "read_only" {
		t.Fatalf("created=%+v", created)
	}
	request := <-requests
	state := request.StateString()
	if len(state) > 32*1024 || !utf8.ValidString(state) || !strings.HasPrefix(state, "Analyze ") || !strings.HasSuffix(state, " final question") {
		t.Fatalf("invalid capped state, len=%d", len(state))
	}
	sess, err := s.sessionManager.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.IsAutoReadOnly() || s.sessionToResponse(sess).RunModeDecision.Label != "read_only" || s.sessionListItem(sess).RunModeDecision.Label != "read_only" || calls.Load() != 1 {
		t.Fatal("decision not persisted/exposed or classified more than once")
	}
}

func TestAutoRunModeDeadlineMetrics(t *testing.T) {
	s, calls, _ := runModeTestServer(t, 200, `{}`)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	req := &CreateSessionRequest{Task: "Analyze", QueueMode: "auto"}
	sess := session.New("test")
	s.applySessionRunMode(ctx, req, sess)
	if !req.Queued || calls.Load() != 0 || sess.Metadata["run_mode_attempt_count"] != float64(1) || sess.Metadata["run_mode_last_outcome"] != "timeout" {
		t.Fatalf("deadline did not fail safely: %+v %v", req, sess.Metadata)
	}
}

func TestAutoRunModeRemoteTargetsQueue(t *testing.T) {
	for _, key := range []string{"unified_agent_id", "docker_agent_id"} {
		s, calls, _ := runModeTestServer(t, 200, `{"answers":{"run_mode":{"type":"choice","choice":"read_only","confidence":0.99}}}`)
		sess := session.New("test")
		sess.Metadata[key] = "remote-target"
		req := &CreateSessionRequest{Task: "Analyze", QueueMode: "auto"}
		s.applySessionRunMode(context.Background(), req, sess)
		if !req.Queued || sess.IsAutoReadOnly() || calls.Load() != 1 {
			t.Fatal("remote tools could bypass the read-only guard")
		}
	}
}
