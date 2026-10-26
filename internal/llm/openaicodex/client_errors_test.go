package openaicodex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/retry"
)

func TestHTTPErrorRetrySafety(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 408, 409, 422, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"request failed for this model","code":"model_not_found"}}`))
			}))
			defer upstream.Close()
			client := NewClientWithOptions("token", "gpt-5.5", upstream.URL, Options{})
			resp, err := client.Chat(context.Background(), &llm.ChatRequest{})
			if err == nil || resp != nil || !strings.Contains(err.Error(), "request failed for this model") {
				t.Fatalf("response=%+v error=%v", resp, err)
			}
			permanent := status < 500 && status != 408 && status != 409 && status != 429
			if got := llm.IsNonRetryable(err); got != permanent {
				t.Fatalf("non-retryable=%v, want %v: %v", got, permanent, err)
			}
			if permanent && retry.IsRetryableError(context.Background(), err) {
				t.Fatalf("permanent HTTP %d was retryable: %v", status, err)
			}
		})
	}
}
