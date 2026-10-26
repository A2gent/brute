package fallback

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/openaicodex"
)

func TestCodexPermanentErrorsPreserveFallbackPolicy(t *testing.T) {
	for _, tc := range []struct {
		status   int
		message  string
		fallback bool
	}{
		{400, "Request failed: model_not_found", false},
		{401, "Request failed: authentication failed", true},
		{402, "Request failed: billing required", true},
		{403, "Request failed: forbidden", true},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(http.StatusText(tc.status)+map[bool]string{false: "/chat", true: "/stream"}[stream], func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": tc.message}})
				}))
				defer upstream.Close()
				backup := &countingLLM{}
				chain := NewClient([]Node{
					{Name: "codex", Client: openaicodex.NewClientWithOptions("token", "gpt-5.5", upstream.URL, openaicodex.Options{})},
					{Name: "backup", Client: backup},
				}, WithMaxRetries(2))
				ctx := WithSessionBoundary(context.Background())
				var err error
				if stream {
					_, err = chain.ChatStream(ctx, &llm.ChatRequest{}, nil)
				} else {
					_, err = chain.Chat(ctx, &llm.ChatRequest{})
				}
				var next *TransitionRequired
				if errors.As(err, &next) != tc.fallback {
					t.Fatalf("fallback=%v, want %v: %v", next, tc.fallback, err)
				}
				if calls.Load() != 1 || backup.calls != 0 {
					t.Fatalf("retried source or contacted fallback before approval: source=%d backup=%d", calls.Load(), backup.calls)
				}
				if tc.fallback {
					chain.CommitTransition(next.Index)
					if _, err := chain.Chat(ctx, &llm.ChatRequest{}); err != nil || backup.calls != 1 {
						t.Fatalf("fallback did not resume: calls=%d error=%v", backup.calls, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), tc.message) {
					t.Fatalf("lost provider error: %v", err)
				}
			})
		}
	}
}
