package http

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

type fakeSessionSummaryClassifier struct {
	response    jev.SystemOneResponse
	err         error
	nilResponse bool
	requests    []jev.SystemOneRequest
}

func (f *fakeSessionSummaryClassifier) SystemOne(_ context.Context, request jev.SystemOneRequest) (*jev.SystemOneResponse, error) {
	f.requests = append(f.requests, request)
	if f.nilResponse {
		return nil, f.err
	}
	return &f.response, f.err
}

func TestSessionSummaryStillAccurate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		probability float64
		missing     bool
		err         error
		want        bool
	}{
		{name: "skip", probability: .99, want: true},
		{name: "threshold inclusive", probability: .8, want: true},
		{name: "regenerate", probability: .799},
		{name: "error", err: errors.New("unavailable")},
		{name: "missing answer", missing: true},
		{name: "invalid probability", probability: 1.1},
		{name: "negative probability", probability: -1},
		{name: "NaN", probability: math.NaN()},
		{name: "infinity", probability: math.Inf(1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeSessionSummaryClassifier{err: tc.err}
			if !tc.missing {
				client.response.Answers = map[string]jev.Answer{"accurate": {Noul: &tc.probability}}
			}
			if got := sessionSummaryStillAccurate(context.Background(), client, nil, "Existing summary", "Latest request", "Final result"); got != tc.want {
				t.Fatalf("reuse = %v, want %v", got, tc.want)
			}
			if len(client.requests) != 1 {
				t.Fatalf("requests = %d, want exactly one", len(client.requests))
			}
			request := client.requests[0]
			question := request.Questions["accurate"]
			if len(request.Questions) != 1 || question.Type != "noul" || question.Instructions != sessionSummaryGateQuestion {
				t.Fatalf("unexpected questions: %+v", request.Questions)
			}
			if request.StateString() != "Existing summary:\nExisting summary\nLatest user message:\nLatest request\nFinal assistant text:\nFinal result" {
				t.Fatalf("unexpected state: %q", request.StateString())
			}
		})
	}
}

func TestSessionSummaryGateEgressAndCap(t *testing.T) {
	long := strings.Repeat("界", sessionSummaryGateMaxBytes)
	for _, tc := range []struct {
		name      string
		summary   string
		user      string
		assistant string
		secrets   []string
		wantCalls int
	}{
		{name: "empty summary", user: "Request", assistant: "Result"},
		{name: "credentials in summary", summary: "password=hunter2"},
		{name: "credentials in user", summary: "Summary", user: "token=abc"},
		{name: "credentials in assistant", summary: "Summary", assistant: "https://localhost/private"},
		{name: "configured credential", summary: "Summary", user: "unlabelled-private-value", secrets: []string{"unlabelled-private-value"}},
		{name: "credential hidden by cap", summary: "Summary", user: long + " token=abc " + long},
		{name: "invalid UTF8", summary: "Summary", assistant: string([]byte{0xff})},
		{name: "bounded UTF8", summary: "SUMMARY_HEAD " + long + " SUMMARY_TAIL", user: "USER_HEAD " + long + " USER_TAIL", assistant: "ASSISTANT_HEAD " + long + " ASSISTANT_TAIL", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeSessionSummaryClassifier{}
			sessionSummaryStillAccurate(context.Background(), client, tc.secrets, tc.summary, tc.user, tc.assistant)
			if len(client.requests) != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", len(client.requests), tc.wantCalls)
			}
			for _, request := range client.requests {
				state := request.StateString()
				if len(state) > sessionSummaryGateMaxBytes || !utf8.ValidString(state) {
					t.Fatalf("state is not bounded UTF8: bytes=%d", len(state))
				}
				for _, marker := range []string{"SUMMARY_HEAD", "SUMMARY_TAIL", "USER_HEAD", "USER_TAIL", "ASSISTANT_HEAD", "ASSISTANT_TAIL"} {
					if !strings.Contains(state, marker) {
						t.Errorf("state lost %s", marker)
					}
				}
			}
		})
	}
	client := &fakeSessionSummaryClassifier{nilResponse: true}
	if sessionSummaryStillAccurate(context.Background(), client, nil, "Summary", "Request", "Result") {
		t.Fatal("nil response must fail open")
	}
	if sessionSummaryStillAccurate(context.Background(), nil, nil, "Summary", "Request", "Result") {
		t.Fatal("missing client must fail open")
	}
}

func newSessionSummaryGateTestServer(t *testing.T, jevStatus int, jevBody string) (*Server, *session.Session, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	jevCalls, llmCalls := &atomic.Int32{}, &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			jevCalls.Add(1)
			w.WriteHeader(jevStatus)
			_, _ = w.Write([]byte(jevBody))
			return
		}
		llmCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		response, _ := json.Marshal(map[string]interface{}{
			"type": "response.completed",
			"response": map[string]interface{}{
				"id": "resp_summary", "status": "completed",
				"output": []map[string]interface{}{{"type": "message", "content": []map[string]string{{"type": "output_text", "text": "Generated LLM summary"}}}},
			},
		})
		_, _ = w.Write([]byte("data: " + string(response) + "\n\n"))
	}))
	t.Cleanup(upstream.Close)
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SaveSettings(map[string]string{sessionSummaryPromptTemplateSettingKey: "Summarize {{latest_assistant_message}}"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.ActiveProvider = string(config.ProviderOpenAI)
	cfg.DefaultModel = "test-model"
	cfg.LLMRetries = 1
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{APIKey: "summary-provider-key", BaseURL: upstream.URL + "/v1", Model: "test-model"}
	cfg.Providers["jev"] = config.Provider{APIKey: "classifier-key", BaseURL: upstream.URL + "/v1"}
	manager := session.NewManager(store)
	sess, err := manager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("Initial request")
	sess.AddAssistantMessage("Initial answer", nil)
	sess.SetStatus(session.StatusCompleted)
	if err := manager.Save(sess); err != nil {
		t.Fatal(err)
	}
	s := &Server{config: cfg, store: store, sessionManager: manager}
	t.Cleanup(s.sessionSummaryWG.Wait)
	return s, sess, jevCalls, llmCalls
}

func refreshSummaryAndReload(t *testing.T, s *Server, sess *session.Session) *session.Session {
	t.Helper()
	s.refreshSessionSummaryAsync(sess)
	s.sessionSummaryWG.Wait()
	fresh, err := s.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fresh
}

func TestRefreshSessionSummaryGate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		unsafe    bool
		failed    bool
		noClient  bool
		wantJev   int32
		wantLLM   int32
		wantSkips uint64
	}{
		{name: "skip", status: 200, body: `{"answers":{"accurate":{"noul":0.8}}}`, wantJev: 1, wantLLM: 1, wantSkips: 1},
		{name: "failed run skip", status: 200, body: `{"answers":{"accurate":{"noul":0.95}}}`, failed: true, wantJev: 1, wantLLM: 1, wantSkips: 1},
		{name: "regenerate", status: 200, body: `{"answers":{"accurate":{"noul":0.79}}}`, wantJev: 1, wantLLM: 2},
		{name: "error fail open", status: 503, body: `unavailable`, wantJev: 1, wantLLM: 2},
		{name: "missing answer fail open", status: 200, body: `{"answers":{}}`, wantJev: 1, wantLLM: 2},
		{name: "malformed response fail open", status: 200, body: `{`, wantJev: 1, wantLLM: 2},
		{name: "unsafe fail open", status: 200, body: `{"answers":{"accurate":{"noul":1}}}`, unsafe: true, wantLLM: 2},
		{name: "unconfigured fail open", noClient: true, wantLLM: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sess, jevCalls, llmCalls := newSessionSummaryGateTestServer(t, tc.status, tc.body)
			if tc.noClient {
				delete(s.config.Providers, "jev")
			}
			// An existing deterministic summary is not proof of LLM provenance.
			sess.SetSummary("Persisted fallback")
			sess = refreshSummaryAndReload(t, s, sess)
			if jevCalls.Load() != 0 || llmCalls.Load() != 1 {
				t.Fatalf("first run should generate directly: jev=%d llm=%d", jevCalls.Load(), llmCalls.Load())
			}
			sess.AddUserMessage("Latest request")
			answer := "A different final answer"
			if tc.unsafe {
				answer = "password=hunter2"
			}
			sess.AddAssistantMessage(answer, nil)
			if tc.failed {
				sess.SetStatus(session.StatusFailed)
			}
			if err := s.sessionManager.Save(sess); err != nil {
				t.Fatal(err)
			}
			sess = refreshSummaryAndReload(t, s, sess)
			if jevCalls.Load() != tc.wantJev || llmCalls.Load() != tc.wantLLM || s.sessionSummarySkipCount != tc.wantSkips {
				t.Fatalf("jev=%d llm=%d skips=%d, want %d/%d/%d", jevCalls.Load(), llmCalls.Load(), s.sessionSummarySkipCount, tc.wantJev, tc.wantLLM, tc.wantSkips)
			}
			if sess.Summary != "Generated LLM summary" {
				t.Fatalf("LLM summary was replaced with fallback: %q", sess.Summary)
			}
		})
	}
}

func TestSessionSummaryPeriodicRegeneration(t *testing.T) {
	for _, interval := range []string{"", "2", "1", "0", "invalid"} {
		t.Run("interval="+interval, func(t *testing.T) {
			s, sess, jevCalls, llmCalls := newSessionSummaryGateTestServer(t, 200, `{"answers":{"accurate":{"noul":1}}}`)
			if err := s.store.SaveSettings(map[string]string{
				sessionSummaryRefreshIntervalSettingKey: interval,
				sessionSummaryPromptTemplateSettingKey:  "Summarize {{latest_assistant_message}}",
			}); err != nil {
				t.Fatal(err)
			}
			m := s.sessionSummaryRefreshInterval()
			if interval != "1" && interval != "2" && m != 5 {
				t.Fatalf("invalid interval did not default to 5: %d", m)
			}
			for run := 0; run <= m; run++ {
				sess = refreshSummaryAndReload(t, s, sess)
				if run < m && llmCalls.Load() != 1 {
					t.Fatalf("unexpected regeneration on run %d: %d calls", run, llmCalls.Load())
				}
			}
			if llmCalls.Load() != 2 || jevCalls.Load() != int32(m-1) || s.sessionSummarySkipCount != uint64(m-1) {
				t.Fatalf("periodic refresh: llm=%d jev=%d skips=%d", llmCalls.Load(), jevCalls.Load(), s.sessionSummarySkipCount)
			}
			if s.sessionSummaryHistory[sess.ID].runs != 0 {
				t.Fatal("successful forced regeneration must reset run counter")
			}
		})
	}
}

func TestSessionSummaryWithoutPromptOrTerminalRun(t *testing.T) {
	s, sess, jevCalls, llmCalls := newSessionSummaryGateTestServer(t, 200, `{"answers":{"accurate":{"noul":1}}}`)
	if err := s.store.SaveSettings(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	sess = refreshSummaryAndReload(t, s, sess)
	if sess.Summary != "Initial answer" || jevCalls.Load() != 0 || llmCalls.Load() != 0 {
		t.Fatalf("without prompt: summary=%q jev=%d llm=%d", sess.Summary, jevCalls.Load(), llmCalls.Load())
	}
	if len(s.sessionSummaryHistory) != 0 || len(s.sessionSummaryLatest) != 0 {
		t.Fatal("fallback-only sessions must not accumulate summary state")
	}
	sess.SetStatus(session.StatusPaused)
	sess.AddAssistantMessage("Paused answer", nil)
	s.refreshSessionSummaryAsync(sess)
	if sess.Summary != "Initial answer" || len(s.sessionSummaryLatest) != 0 {
		t.Fatal("non-terminal runs must not refresh summaries")
	}
}

func TestSessionSummaryRestartRegeneratesConservatively(t *testing.T) {
	s, sess, jevCalls, llmCalls := newSessionSummaryGateTestServer(t, 200, `{"answers":{"accurate":{"noul":1}}}`)
	sess = refreshSummaryAndReload(t, s, sess)
	// The persisted summary has no durable source marker. A new server must
	// generate instead of assuming that any nonempty stored summary came from an LLM.
	restarted := &Server{config: s.config, store: s.store, sessionManager: s.sessionManager}
	refreshSummaryAndReload(t, restarted, sess)
	if llmCalls.Load() != 2 || jevCalls.Load() != 0 {
		t.Fatalf("restart: llm=%d jev=%d", llmCalls.Load(), jevCalls.Load())
	}
}
