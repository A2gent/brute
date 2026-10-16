package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
)

func TestSessionResponseLMStudioContextWindow(t *testing.T) {
	for _, tt := range []struct {
		name     string
		body     string
		status   int
		override int
		want     int
	}{
		{name: "unloaded model maximum", body: `{"models":[{"type":"llm","key":"qwen/qwen3.8-27b","max_context_length":262144}]}`, want: 262144},
		{name: "loaded instance limit", body: `{"models":[{"type":"llm","key":"qwen/qwen3.8-27b","max_context_length":262144,"loaded_instances":[{"id":"qwen/qwen3.8-27b","config":{"context_length":4096}}]}]}`, want: 4096},
		{name: "API unavailable", status: http.StatusNotFound, want: 32768},
		{name: "configured override", override: 65536, want: 65536},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.override > 0 {
					t.Error("explicit context override should not query LM Studio")
				}
				if r.URL.Path != "/api/v1/models" {
					t.Errorf("request path = %s", r.URL.Path)
				}
				if tt.status > 0 {
					w.WriteHeader(tt.status)
				}
				w.Write([]byte(tt.body))
			}))
			defer api.Close()
			cfg := config.DefaultConfig()
			cfg.Providers["lmstudio"] = config.Provider{BaseURL: api.URL + "/v1", ContextWindow: tt.override}
			s := &Server{config: cfg}
			for _, provider := range []string{"lmstudio", "fallback", "auto_router"} {
				metadata := map[string]interface{}{
					"provider": provider, "model": "qwen/qwen3.8-27b", "context_window": 32768,
				}
				if provider == "fallback" {
					metadata["fallback_active_provider"] = "lmstudio"
					metadata["fallback_active_model"] = "qwen/qwen3.8-27b"
				}
				if provider == "auto_router" {
					metadata["routed_provider"] = "lmstudio"
					metadata["routed_model"] = "qwen/qwen3.8-27b"
				}
				response := s.sessionToResponse(&session.Session{ID: "old-session", Metadata: metadata})
				if response.ModelContextWindow != tt.want {
					t.Errorf("%s response context = %d, want %d", provider, response.ModelContextWindow, tt.want)
				}
			}
		})
	}
}
