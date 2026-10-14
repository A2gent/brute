package lmstudio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestChatStreamUsage(t *testing.T) {
	wantUsage := llm.TokenUsage{InputTokens: 2048, OutputTokens: 100, CachedInputTokens: 1024, ReasoningTokens: 80}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream        bool `json:"stream"`
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if !body.Stream || body.StreamOptions == nil || !body.StreamOptions.IncludeUsage {
			t.Errorf("expected streaming request with include_usage=true, got %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Hello "}}],"usage":null}

data: {"choices":[{"delta":{"content":"world","refusal":"!"},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":2048,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":1024},"completion_tokens_details":{"reasoning_tokens":80}}}

data: [DONE]

`)
	}))
	defer server.Close()

	for _, withCallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("callback=%t", withCallback), func(t *testing.T) {
			var events []llm.StreamEvent
			var callback func(llm.StreamEvent) error
			if withCallback {
				callback = func(event llm.StreamEvent) error {
					events = append(events, event)
					return nil
				}
			}
			result, err := NewClient("", "test-model", server.URL).ChatStream(context.Background(), &llm.ChatRequest{}, callback)
			if err != nil {
				t.Fatal(err)
			}
			if result.Usage != wantUsage {
				t.Errorf("usage = %+v, want %+v", result.Usage, wantUsage)
			}
			if result.Content != "Hello world!" || result.StopReason != "stop" {
				t.Errorf("unexpected response: %+v", result)
			}
			if withCallback {
				if len(events) != 4 || events[3].Type != llm.StreamEventUsage || events[3].Usage != wantUsage {
					t.Errorf("unexpected stream events: %+v", events)
				}
			}
		})
	}
}

func TestChatReasoningUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if _, ok := body["stream_options"]; ok {
			t.Error("non-streaming request must omit stream_options")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2048,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":1024},"completion_tokens_details":{"reasoning_tokens":80}}}`)
	}))
	defer server.Close()
	result, err := NewClient("", "test-model", server.URL).Chat(context.Background(), &llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	want := llm.TokenUsage{InputTokens: 2048, OutputTokens: 100, CachedInputTokens: 1024, ReasoningTokens: 80}
	if result.Usage != want {
		t.Errorf("usage = %+v, want %+v", result.Usage, want)
	}
}

func TestNewClientSharedTransport(t *testing.T) {
	client := NewClient("", "test-model", "")
	if client.httpClient.Transport != llm.SharedHTTPTransport(0) {
		t.Error("client must use shared transport with response header timeout")
	}
}
