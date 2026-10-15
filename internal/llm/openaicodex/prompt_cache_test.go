package openaicodex

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestPromptCacheRetentionRequestField(t *testing.T) {
	for _, tc := range []struct {
		name, model, requestModel, retention string
		public                               bool
		want                                 interface{}
	}{
		{name: "public default", model: "gpt-5.4", public: true, want: "24h"},
		{name: "public dated snapshot", model: "gpt-5.1-2025-11-13", public: true, want: "24h"},
		{name: "public in_memory", model: "gpt-5.4", retention: "in_memory", public: true, want: "in_memory"},
		{name: "gpt-5.5 rejects in_memory", model: "gpt-5.5", retention: "in_memory", public: true},
		{name: "unsupported model", model: "gpt-5.4-mini", public: true},
		{name: "request model override", model: "gpt-5.4", requestModel: "gpt-5.4-mini", public: true},
		{name: "codex backend", model: "gpt-5.4", retention: "24h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]interface{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"resp_1","status":"completed","output":[]}`)
			}))
			defer server.Close()
			c := NewClientWithOptions("key", tc.model, server.URL, Options{PublicAPI: tc.public, PromptCacheRetention: tc.retention})
			if _, err := c.Chat(t.Context(), &llm.ChatRequest{Model: tc.requestModel}); err != nil {
				t.Fatal(err)
			}
			if got := body["prompt_cache_retention"]; got != tc.want {
				t.Fatalf("prompt_cache_retention = %v, want %v", got, tc.want)
			}
		})
	}
}
