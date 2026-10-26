package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleListProviderTransitionsProxiesDockerChildSession(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sessions/docker-child/provider-transitions" {
			t.Errorf("unexpected delegated transition request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"version":     1,
			"transitions": []map[string]interface{}{{"version": 1, "id": "remote-transition"}},
		})
	}))
	defer child.Close()
	stubDockerPS(t, child.URL, "agent-dev-reviewer")

	req := httptest.NewRequest(http.MethodGet, "/sessions/docker-child/provider-transitions", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected proxied transitions, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Version     int `json:"version"`
		Transitions []struct {
			ID string `json:"id"`
		} `json:"transitions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to decode proxied transitions: %v", err)
	}
	if result.Version != 1 || len(result.Transitions) != 1 || result.Transitions[0].ID != "remote-transition" {
		t.Fatalf("unexpected proxied transitions: %+v", result)
	}
}

func TestHandleListProviderTransitionsDistinguishesDelegatedLookupFailures(t *testing.T) {
	for _, test := range []struct {
		name         string
		upstreamCode int
		wantCode     int
	}{
		{name: "child session absent", upstreamCode: http.StatusNotFound, wantCode: http.StatusNotFound},
		{name: "child agent unavailable", upstreamCode: http.StatusServiceUnavailable, wantCode: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newUnifiedAgentsTestServer(t)
			child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.upstreamCode)
			}))
			defer child.Close()
			stubDockerPS(t, child.URL, "agent-dev-reviewer")

			req := httptest.NewRequest(http.MethodGet, "/sessions/docker-child/provider-transitions", nil)
			rec := httptest.NewRecorder()
			server.router.ServeHTTP(rec, req)
			if rec.Code != test.wantCode {
				t.Fatalf("expected %d, got %d: %s", test.wantCode, rec.Code, rec.Body.String())
			}
		})
	}
}
