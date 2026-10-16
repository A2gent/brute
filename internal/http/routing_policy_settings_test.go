package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/storage"
)

func TestRoutingPolicySettingsRoundTrip(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProviderTransitionMode = "confirm"
	s := newProviderTestServer(t, cfg)
	custom := s.store.(storage.CustomEnvStore)
	if err := custom.SaveSettingsAndCustomEnv(map[string]string{"AAGENT_NAME": "keep", "AAGENT_GIT_COMMIT_PROVIDER": "hidden"}, map[string]string{"ROUTING_TEST_ENV": "keep-env"}); err != nil {
		t.Fatal(err)
	}
	call := func(method, body string) map[string]interface{} {
		t.Helper()
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, httptest.NewRequest(method, "/settings", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var result map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := call("GET", "")["effective_provider_transition_mode"]; got != "confirm" {
		t.Fatalf("config mode: %v", got)
	}
	cfg.ProviderTransitionMode = ""
	if got := call("GET", "")["effective_provider_transition_mode"]; got != "confirm" {
		t.Fatalf("default: %v", got)
	}
	for _, mode := range []string{"compact_auto", "switch_auto", "confirm", "linked_session"} {
		call("PUT", `{"merge":true,"settings":{"A2GENT_PROVIDER_TRANSITION_MODE":"`+mode+`"}}`)
		result := call("GET", "")
		if result["effective_provider_transition_mode"] != mode {
			t.Fatalf("effective: %v", result)
		}
		saved, _ := s.store.GetSettings()
		env, _ := custom.GetCustomEnv()
		if saved["A2GENT_PROVIDER_TRANSITION_MODE"] != mode || saved["AAGENT_NAME"] != "keep" || saved["AAGENT_GIT_COMMIT_PROVIDER"] != "hidden" || env["ROUTING_TEST_ENV"] != "keep-env" || env["A2GENT_PROVIDER_TRANSITION_MODE"] != "" {
			t.Fatalf("settings=%v env=%v", saved, env)
		}
	}
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("PUT", "/settings", strings.NewReader(`{"merge":true,"settings":{"A2GENT_PROVIDER_TRANSITION_MODE":"invalid"}}`)))
	if rec.Code != 400 {
		t.Fatalf("invalid mode: %d", rec.Code)
	}
}

func TestRoutingConfirmationGatesManualSelection(t *testing.T) {
	for _, override := range []string{"", "confirm"} {
		t.Run("override="+override, func(t *testing.T) {
			testRoutingConfirmationGatesManualSelection(t, override)
		})
	}
}

func testRoutingConfirmationGatesManualSelection(t *testing.T, override string) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read-only model discovery is allowed before confirmation; invoking the
		// target model must still wait for approval.
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/models" {
			w.Write([]byte(`{"models":[]}`))
			return
		}
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer target.Close()
	cfg := config.DefaultConfig()
	cfg.Providers["lmstudio"] = config.Provider{BaseURL: target.URL, Model: "target"}
	s := newProviderTestServer(t, cfg)
	if override != "" {
		save := httptest.NewRecorder()
		s.router.ServeHTTP(save, httptest.NewRequest("PUT", "/settings", strings.NewReader(`{"merge":true,"settings":{"A2GENT_PROVIDER_TRANSITION_MODE":"`+override+`"}}`)))
		if save.Code != 200 {
			t.Fatalf("save: %d %s", save.Code, save.Body)
		}
	}
	sess, err := s.sessionManager.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	sess.Metadata = map[string]interface{}{"provider": "lmstudio", "model": "source"}
	if err := s.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("PUT", "/sessions/"+sess.ID+"/provider", strings.NewReader(`{"provider":"lmstudio","model":"target","transition_id":"manual-setting-test"}`)))
	if rec.Code != 202 {
		t.Fatalf("manual: %d %s", rec.Code, rec.Body)
	}
	var transition map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &transition); err != nil {
		t.Fatal(err)
	}
	if transition["phase"] != "awaiting_confirmation" || transition["mode"] != "confirm" {
		t.Fatalf("bypassed confirmation: %v", transition)
	}
	if calls.Load() != 0 {
		t.Fatalf("target contacted before approval: %d", calls.Load())
	}
	rec = httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest("POST", "/sessions/"+sess.ID+"/provider-transitions/manual-setting-test/decision", strings.NewReader(`{"decision":"reject"}`)))
	if rec.Code != 200 || calls.Load() != 0 {
		t.Fatalf("reject: %d, target calls: %d", rec.Code, calls.Load())
	}
}
