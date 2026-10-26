package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"github.com/A2gent/brute/internal/llm/openaicodex"
	"github.com/A2gent/brute/internal/llm/retry"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// Hide ChatStream to exercise the proxy's non-streaming provider path too.
type proxyChatOnlyClient struct{ llm.Client }

func TestLLMProxyProviderErrorsReachAgentWithoutEmptyRetry(t *testing.T) {
	for _, tc := range []struct {
		name, code, message string
		status              int
	}{
		{"model", "model_not_found", "The 'gpt-5.5' model is not supported when using Codex with a ChatGPT account.", 400},
		{"request", "invalid_request_error", "Request failed: invalid tool schema", 400},
		{"auth", "invalid_api_key", "Authentication failed: invalid API key", 401},
		{"forbidden", "permission_denied", "This account cannot access this model", 403},
	} {
		for _, streaming := range []bool{false, true} {
			mode := "chat"
			if streaming {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				var providerCalls, proxyCalls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					providerCalls.Add(1)
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": tc.code, "message": tc.message}})
				}))
				defer upstream.Close()
				producer := retry.Wrap(openaicodex.NewClientWithOptions("token", "gpt-5.5", upstream.URL, openaicodex.Options{}))
				if !streaming {
					producer = proxyChatOnlyClient{producer}
				}
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					proxyCalls.Add(1)
					(&Server{}).handleLLMProxyChatCompletionsStream(w, r, producer, &llm.ChatRequest{}, "gpt-5.5")
				}))
				defer proxy.Close()
				store, err := storage.NewSQLiteStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				manager := session.NewManager(store)
				sess, err := manager.Create("test-agent")
				if err != nil {
					t.Fatal(err)
				}
				consumer := retry.Wrap(lmstudio.NewClient("", "gpt-5.5", proxy.URL))
				runner := agent.New(agent.Config{MaxSteps: 5, ContextWindow: 10000}, consumer, tools.NewManager(t.TempDir()), manager)
				_, _, err = runner.RunWithEvents(context.Background(), sess, "review changes", nil)
				if err == nil || !strings.Contains(err.Error(), tc.message) || !strings.Contains(err.Error(), tc.code) {
					t.Fatalf("expected original provider error, got %v", err)
				}
				if !llm.IsNonRetryable(err) {
					t.Fatalf("retry safety lost across proxy: %v", err)
				}
				if providerCalls.Load() != 1 || proxyCalls.Load() != 1 {
					t.Fatalf("unexpected retry: provider calls=%d proxy calls=%d", providerCalls.Load(), proxyCalls.Load())
				}
				if sess.Status != session.StatusFailed {
					t.Fatalf("status=%s, want failed", sess.Status)
				}
				for _, msg := range sess.Messages {
					if strings.Contains(msg.Content, "previous model response was empty") || strings.Contains(msg.Content, "empty final response") {
						t.Fatalf("provider error became an empty-response retry: %s", msg.Content)
					}
				}
			})
		}
	}
}

func TestLocalAgentStreamPreservesProviderError(t *testing.T) {
	const providerError = `LLM error: OpenAI Codex error (400): {"error":{"code":"model_not_found","message":"The 'gpt-5.5' model is not supported when using Codex with a ChatGPT account."}}`
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_ = json.NewEncoder(w).Encode(ChatStreamEvent{Type: "error", Error: providerError, Status: "failed"})
	}))
	defer child.Close()
	response, err := postLocalDockerAgentChatStream(context.Background(), child.Client(), child.URL+"/chat/stream", ChatRequest{Message: "review changes"}, nil)
	if err == nil || err.Error() != providerError || response.Status != "failed" {
		t.Fatalf("provider error lost in child relay: response=%+v error=%v", response, err)
	}
}

type proxyErrorClient struct{ err error }

func (c proxyErrorClient) Chat(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, c.err
}

func TestLLMProxyStreamPreservesRetryMarkers(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"transient", errors.New("provider unavailable")},
		{"non_retryable", llm.NonRetryable(errors.New("invalid request"))},
		{"unsafe", llm.UnsafeForRetry(errors.New("tool may have executed"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				(&Server{}).handleLLMProxyChatCompletionsStream(w, r, proxyErrorClient{tc.err}, &llm.ChatRequest{}, "model")
			}))
			defer proxy.Close()
			_, err := lmstudio.NewClient("", "model", proxy.URL).ChatStream(context.Background(), &llm.ChatRequest{}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.err.Error()) {
				t.Fatalf("provider error lost: %v", err)
			}
			if llm.IsUnsafeForRetry(err) != llm.IsUnsafeForRetry(tc.err) || llm.IsNonRetryable(err) != llm.IsNonRetryable(tc.err) {
				t.Fatalf("retry and execution-safety markers changed in transit: %v", err)
			}
		})
	}
}
