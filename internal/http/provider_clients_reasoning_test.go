package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
)

func TestSelectRoutingRuleViaLLMAppliesRouterReasoningEffort(t *testing.T) {
	var requestBody map[string]interface{}
	upstream := newReasoningTestCodexServer(t, &requestBody, `{"index":1,"reason":"coding"}`)

	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:    string(config.ProviderOpenAICodex),
		APIKey:  "test-key",
		BaseURL: upstream.URL,
		Model:   "gpt-5.6-codex",
	}
	server := &Server{config: cfg}
	autoCfg := config.Provider{
		RouterProvider:        string(config.ProviderOpenAICodex),
		RouterModel:           "gpt-5.6-codex",
		RouterReasoningEffort: "high",
	}
	rules := []config.RouterRule{
		{Match: "coding", Provider: string(config.ProviderOpenAICodex), Model: "gpt-5.6-codex"},
		{Match: "docs", Provider: string(config.ProviderOpenAICodex), Model: "gpt-5.6-codex"},
	}

	if _, _, err := server.selectRoutingRuleViaLLM(context.Background(), "fix tests", autoCfg, rules); err != nil {
		t.Fatalf("selectRoutingRuleViaLLM failed: %v", err)
	}
	assertReasoningEffort(t, requestBody, "high")
}

func TestCreateFallbackChainClientAppliesNodeReasoningEffort(t *testing.T) {
	var requestBody map[string]interface{}
	upstream := newReasoningTestCodexServer(t, &requestBody, "ok")

	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		Name:    string(config.ProviderOpenAICodex),
		APIKey:  "test-key",
		BaseURL: upstream.URL,
		Model:   "gpt-5.6-codex",
	}
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		Name:    string(config.ProviderOpenAI),
		APIKey:  "test-key",
		BaseURL: upstream.URL,
		Model:   "gpt-5.5",
	}
	cfg.Providers[string(config.ProviderFallback)] = config.Provider{
		FallbackChainNodes: []config.FallbackChainNode{
			{
				Provider:        string(config.ProviderOpenAICodex),
				Model:           "gpt-5.6-codex",
				ReasoningEffort: "xhigh",
			},
			{Provider: string(config.ProviderOpenAI), Model: "gpt-5.5"},
		},
	}
	server := &Server{config: cfg}

	client, err := server.createFallbackChainClient(config.ProviderFallback, nil)
	if err != nil {
		t.Fatalf("createFallbackChainClient failed: %v", err)
	}
	if _, err := client.Chat(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "fix tests"}},
	}); err != nil {
		t.Fatalf("fallback chat failed: %v", err)
	}
	assertReasoningEffort(t, requestBody, "xhigh")
}

func newReasoningTestCodexServer(t *testing.T, requestBody *map[string]interface{}, content string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":" + mustJSONReasoningTest(t, content) + "}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	t.Cleanup(server.Close)
	return server
}

func mustJSONReasoningTest(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertReasoningEffort(t *testing.T, requestBody map[string]interface{}, want string) {
	t.Helper()
	reasoning, _ := requestBody["reasoning"].(map[string]interface{})
	if got, _ := reasoning["effort"].(string); got != want {
		t.Fatalf("reasoning effort = %q, want %q; request=%#v", got, want, requestBody)
	}
}
