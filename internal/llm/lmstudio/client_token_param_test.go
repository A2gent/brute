package lmstudio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestModelRequiresMaxCompletionTokens(t *testing.T) {
	cases := map[string]bool{
		"gpt-5.5":               true,
		"gpt-5.3-codex":         true,
		"openai/gpt-5.5":        true, // router-prefixed
		"o1-mini":               true,
		"o3":                    true,
		"o4-mini":               true,
		"gpt-4.1":               false,
		"gpt-4o-mini":           false,
		"qwen2.5-coder-32b":     false,
		"anthropic/claude-opus": false,
	}
	for model, want := range cases {
		if got := modelRequiresMaxCompletionTokens(model); got != want {
			t.Errorf("modelRequiresMaxCompletionTokens(%q) = %v, want %v", model, got, want)
		}
	}
}

func captureRequestBody(t *testing.T, model string) map[string]any {
	t.Helper()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	defer server.Close()

	client := NewClient("sk-test", model, server.URL)
	if _, err := client.Chat(context.Background(), &llm.ChatRequest{
		Model:       model,
		Messages:    []llm.Message{{Role: "user", Content: "hi"}},
		MaxTokens:   1234,
		Temperature: 0.7,
	}); err != nil {
		t.Fatalf("Chat(%q): %v", model, err)
	}
	return captured
}

func TestChatUsesMaxCompletionTokensForGPT5(t *testing.T) {
	body := captureRequestBody(t, "gpt-5.5")
	if _, ok := body["max_tokens"]; ok {
		t.Errorf("gpt-5.5 request must not include max_tokens: %v", body)
	}
	if got := body["max_completion_tokens"]; got != float64(1234) {
		t.Errorf("expected max_completion_tokens=1234, got %v", body["max_completion_tokens"])
	}
	// GPT-5 family only accepts the default temperature; it must be omitted.
	if _, ok := body["temperature"]; ok {
		t.Errorf("gpt-5.5 request must not include temperature: %v", body)
	}
}

func TestChatUsesMaxTokensForLegacyModels(t *testing.T) {
	body := captureRequestBody(t, "gpt-4.1")
	if got := body["max_tokens"]; got != float64(1234) {
		t.Errorf("expected max_tokens=1234, got %v", body["max_tokens"])
	}
	if _, ok := body["max_completion_tokens"]; ok {
		t.Errorf("legacy model must not include max_completion_tokens: %v", body)
	}
	if got := body["temperature"]; got != 0.7 {
		t.Errorf("expected temperature=0.7, got %v", body["temperature"])
	}
}

func TestModelRequiresMaxCompletionTokensCaseInsensitive(t *testing.T) {
	if !modelRequiresMaxCompletionTokens(strings.ToUpper("gpt-5.5")) {
		t.Errorf("expected case-insensitive match for GPT-5.5")
	}
}

func TestOpenAIChatUsesSessionCacheKeyAndReadsCachedTokens(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage": map[string]any{
				"prompt_tokens":         2048,
				"completion_tokens":     10,
				"prompt_tokens_details": map[string]any{"cached_tokens": 1024},
			},
		})
	}))
	defer server.Close()

	client := NewClient("sk-test", "gpt-5.5", server.URL)
	client.baseURL = server.URL
	// providerName detects OpenAI from the URL in production; use the official hostname
	// only for cache-key detection while retaining the local test transport.
	client.httpClient.Transport = rewriteHostTransport{target: server.URL, base: http.DefaultTransport}
	client.baseURL = "https://api.openai.com/v1"
	resp, err := client.Chat(context.Background(), &llm.ChatRequest{
		Model: "gpt-5.5", SessionID: "session-cache-key", Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := captured["prompt_cache_key"]; got != "session-cache-key" {
		t.Fatalf("prompt_cache_key = %#v", got)
	}
	if got := resp.Usage.CachedInputTokens; got != 1024 {
		t.Fatalf("cached input tokens = %d", got)
	}
}

type rewriteHostTransport struct {
	target string
	base   http.RoundTripper
}

func (t rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, _ := url.Parse(t.target)
	clone := req.Clone(req.Context())
	clone.URL.Scheme = target.Scheme
	clone.URL.Host = target.Host
	return t.base.RoundTrip(clone)
}
