package http

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestJevScoreEffort(t *testing.T) {
	for _, tc := range []struct {
		name       string
		score      float64
		confidence float64
		floor      string
		want       string
	}{
		{"lookup", 0, .99, "none", "low"},
		{"small edit", 1, .9, "low", "low"},
		{"multi-file", 2, .85, "minimal", "medium"},
		{"design", 3, .99, "low", "high"},
		{"medium minimum", 0, .99, "medium", "medium"},
		{"high minimum", 1, .99, "high", "high"},
		{"unsupported", 0, .99, "", ""},
		{"uncertain", 0, .84, "low", ""},
		{"negative", -1, .99, "low", ""},
		{"out of range", 4, .99, "low", ""},
		{"fractional", 1.5, .99, "low", ""},
		{"nan score", math.NaN(), .99, "low", ""},
		{"infinite score", math.Inf(1), .99, "low", ""},
		{"nan confidence", 0, math.NaN(), "low", ""},
		{"invalid confidence", 0, 1.1, "low", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jevScoreEffort(jev.Answer{Score: &tc.score, Confidence: tc.confidence}, tc.floor); got != tc.want {
				t.Fatalf("effort = %q, want %q", got, tc.want)
			}
		})
	}
	if got := jevScoreEffort(jev.Answer{Confidence: 1}, "low"); got != "" {
		t.Fatalf("missing score = %q", got)
	}
}

func TestJevEffortCapabilities(t *testing.T) {
	for _, tc := range []struct {
		provider config.ProviderType
		model    string
		want     string
	}{
		{config.ProviderOpenAI, "gpt-5.4", "none"},
		{config.ProviderOpenAICodex, "gpt-5.4", "none"},
		{config.ProviderOpenAI, "gpt-5.1-codex-mini", "medium"},
		{config.ProviderOpenAI, "gpt-5-pro", "high"},
		{config.ProviderOpenAI, "gpt-4o", ""},
		{config.ProviderOpenAI, "gpt-5-chat-latest", ""},
		{config.ProviderGoogle, "gpt-5.4", ""},
		{config.ProviderFallback, "gpt-5.4", ""},
	} {
		if got := jevEffortFloor(tc.provider, tc.model); got != tc.want {
			t.Errorf("%s/%s floor = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

func newJevEffortTestServer(t *testing.T, status int, body string) (*Server, *atomic.Int32, <-chan jev.SystemOneRequest) {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	calls := &atomic.Int32{}
	requests := make(chan jev.SystemOneRequest, 10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request jev.SystemOneRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests <- request
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	cfg := config.DefaultConfig()
	cfg.Tools.JevEffort.Enabled = true
	cfg.Providers["jev"] = config.Provider{APIKey: "jev-test-credential", BaseURL: upstream.URL}
	cfg.Providers["openai"] = config.Provider{APIKey: "openai-test-credential", Model: "gpt-5.4", ReasoningEffort: "high", BaseURL: "http://127.0.0.1:1"}
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Server{config: cfg, store: store, sessionManager: session.NewManager(store)}, calls, requests
}

func resolveJevEffortTestTarget(t *testing.T, server *Server, sess *session.Session, prompt string) *executionTarget {
	t.Helper()
	target, err := server.resolveExecutionTarget(context.Background(), config.ProviderOpenAI, "", prompt, sess)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestJevEffortStickyAndUserOverride(t *testing.T) {
	server, calls, requests := newJevEffortTestServer(t, 200, `{"answers":{"effort":{"type":"score","score":0,"confidence":0.99}}}`)
	sess := session.New("build")
	prompt := "What time is it? " + strings.Repeat("context ", 8000) + "final ask"
	target := resolveJevEffortTestTarget(t, server, sess, prompt)
	if target.ReasoningEffort != "low" || calls.Load() != 1 {
		t.Fatalf("target = %+v, calls = %d", target, calls.Load())
	}
	request := <-requests
	if request.StateString() != llm.HeadTail(prompt, llm.RouterPromptMaxBytes) || len(request.Questions) != 1 || request.Questions["effort"].Type != "score" {
		t.Fatalf("unexpected classifier request: %+v", request)
	}
	if server.agentConfigFromTarget(sess, target, "", 1, 0).ReasoningEffort != "low" {
		t.Fatal("selected effort did not reach agent config")
	}
	if !setSessionRoutedProviderAndModel(sess, config.ProviderOpenAI, target.ProviderType, target.Model, target.RoutingRule, target.RoutingReason) {
		t.Fatal("expected persisted decision for fixed provider")
	}
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	event := routerDecisionStreamEvent(config.ProviderOpenAI, target)
	if event == nil || event.Type != "router_decision" || event.RoutedRule != jevEffortRoutingRule || !strings.Contains(event.RoutedReason, "low") || !strings.Contains(event.RoutedReason, "jev confidence=0.99") {
		t.Fatalf("decision not visible in stream: %+v", event)
	}
	// Reload from disk and change the provider default: neither the next prompt
	// nor a config edit may reclassify this session or invalidate its cache.
	sess, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider := server.config.Providers["openai"]
	provider.ReasoningEffort = "medium"
	server.config.Providers["openai"] = provider
	target = resolveJevEffortTestTarget(t, server, sess, "Refactor the scheduler across all packages")
	if target.ReasoningEffort != "low" || calls.Load() != 1 {
		t.Fatalf("sticky decision lost: %+v, calls=%d", target, calls.Load())
	}
	sess.Metadata["reasoning_effort"] = " high "
	target = resolveJevEffortTestTarget(t, server, sess, "hello")
	if target.ReasoningEffort != "high" || calls.Load() != 1 || routerDecisionStreamEvent(config.ProviderOpenAI, target) != nil {
		t.Fatalf("explicit user effort ignored: %+v, calls=%d", target, calls.Load())
	}
	if !setSessionRoutedProviderAndModel(sess, config.ProviderOpenAI, target.ProviderType, target.Model, target.RoutingRule, target.RoutingReason) {
		t.Fatal("user override must clear the old router decision")
	}
}

func TestJevEffortFailOpenIsSticky(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"http error", 500, "upstream failed"},
		{"malformed response", 200, "{"},
		{"low confidence", 200, `{"answers":{"effort":{"score":0,"confidence":0.3}}}`},
		{"missing answer", 200, `{"answers":{}}`},
		{"missing score", 200, `{"answers":{"effort":{"confidence":1}}}`},
		{"out of range", 200, `{"answers":{"effort":{"score":9,"confidence":1}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, calls, _ := newJevEffortTestServer(t, tc.status, tc.body)
			sess := session.New("build")
			for range 2 {
				target := resolveJevEffortTestTarget(t, server, sess, "hello")
				if target.ReasoningEffort != "high" || !strings.Contains(target.RoutingReason, "configured default") {
					t.Fatalf("not fail open: %+v", target)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("calls=%d, want one attempt per session", calls.Load())
			}
		})
	}
}

func TestJevEffortBypassesClassification(t *testing.T) {
	for _, name := range []string{"disabled", "explicit", "unsupported", "missing credentials", "secret", "secret in capped middle", "empty", "nil session", "auxiliary", "auto router"} {
		t.Run(name, func(t *testing.T) {
			server, calls, _ := newJevEffortTestServer(t, 200, `{"answers":{"effort":{"score":0,"confidence":1}}}`)
			sess := session.New("build")
			prompt := "hello"
			provider := config.ProviderOpenAI
			switch name {
			case "disabled":
				server.config.Tools.JevEffort.Enabled = false
			case "explicit":
				sess.Metadata["reasoning_effort"] = "medium"
			case "unsupported":
				cfg := server.config.Providers["openai"]
				cfg.Model = "gpt-4o"
				server.config.Providers["openai"] = cfg
			case "missing credentials":
				delete(server.config.Providers, "jev")
			case "secret":
				prompt = "inspect openai-test-credential"
			case "secret in capped middle":
				prompt = strings.Repeat("x", 32000) + "openai-test-credential" + strings.Repeat("y", 32000)
			case "empty":
				prompt = " "
			case "nil session":
				sess = nil
			case "auto router":
				provider = config.ProviderAutoRouter
				server.config.Providers["automatic_router"] = config.Provider{RouterProvider: "openai", RouterRules: []config.RouterRule{{Match: "all", Provider: "openai", Model: "gpt-5.4", ReasoningEffort: "medium"}}}
			}
			var target *executionTarget
			var err error
			if name == "auxiliary" {
				target, err = server.resolveExecutionTargetBase(context.Background(), provider, "", prompt, sess)
			} else {
				target, err = server.resolveExecutionTarget(context.Background(), provider, "", prompt, sess)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "high"
			if name == "explicit" || name == "auto router" {
				want = "medium"
			}
			if target.ReasoningEffort != want || calls.Load() != 0 {
				t.Fatalf("target=%+v calls=%d, want effort=%q with no classification", target, calls.Load(), want)
			}
		})
	}
}

func TestJevEffortEmptyDefaultIsSticky(t *testing.T) {
	server, calls, _ := newJevEffortTestServer(t, 500, "failed")
	provider := server.config.Providers["openai"]
	provider.ReasoningEffort = ""
	server.config.Providers["openai"] = provider
	sess := session.New("build")
	for range 2 {
		if got := resolveJevEffortTestTarget(t, server, sess, "hello").ReasoningEffort; got != "" {
			t.Fatalf("empty configured default changed to %q", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1", calls.Load())
	}
}

func TestJevEffortStickyDecisionRespectsNewModelMinimum(t *testing.T) {
	server, calls, _ := newJevEffortTestServer(t, 200, `{"answers":{"effort":{"score":0,"confidence":0.99}}}`)
	sess := session.New("build")
	if got := resolveJevEffortTestTarget(t, server, sess, "hello").ReasoningEffort; got != "low" {
		t.Fatalf("initial effort = %q", got)
	}
	target, err := server.resolveExecutionTarget(context.Background(), config.ProviderOpenAI, "gpt-5-pro", "hello", sess)
	if err != nil {
		t.Fatal(err)
	}
	if target.ReasoningEffort != "high" || calls.Load() != 1 {
		t.Fatalf("effort = %q calls=%d, want high without reclassification", target.ReasoningEffort, calls.Load())
	}
}
