package providerstest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
)

// CheckOpenAIResponses keeps API-key wiring consistent across runtime entry points.
func CheckOpenAIResponses(t *testing.T, factory func(*config.Config) (llm.Client, error)) {
	t.Helper()
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("A2GENT_PARENT_PROXY_URL", "")
	for _, stateful := range []bool{false, true} {
		t.Run(fmt.Sprintf("stateful=%v", stateful), func(t *testing.T) {
			var payload map[string]interface{}
			var headers http.Header
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers = r.Header.Clone()
				if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2048,"input_tokens_details":{"cached_tokens":1024},"output_tokens":100,"output_tokens_details":{"reasoning_tokens":80}}}}`+"\n\n")
			}))
			defer upstream.Close()
			cfg := config.DefaultConfig()
			cfg.ActiveProvider = string(config.ProviderOpenAI)
			cfg.DefaultModel = "gpt-5.4"
			cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
				APIKey: "test-key", BaseURL: upstream.URL + "/v1", Model: cfg.DefaultModel,
				ReasoningEffort: "low", TextVerbosity: "low", ServiceTier: "auto", MaxTokens: 512, PromptCacheKey: "provider-cache", PromptCacheRetention: "in_memory", StatefulResponses: &stateful,
			}
			client, err := factory(cfg)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Chat(t.Context(), &llm.ChatRequest{SystemPrompt: "Test instructions", PreviousResponseID: "resp_previous", Tools: []llm.ToolDefinition{{Name: "lookup", InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"optional": map[string]interface{}{"type": "string"}}}}}, Messages: []llm.Message{{Role: "user", Content: "hello"}}})
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]interface{}{"model": cfg.DefaultModel, "instructions": "Test instructions", "prompt_cache_key": "provider-cache", "prompt_cache_retention": "in_memory", "service_tier": "auto", "max_output_tokens": float64(512), "store": stateful, "stream": true} {
				if payload[key] != want {
					t.Errorf("%s = %v, want %v", key, payload[key], want)
				}
			}
			tools := payload["tools"].([]interface{})
			if tools[0].(map[string]interface{})["strict"] != false {
				t.Error("tool optional parameters must not be strict-normalized")
			}
			effort, _ := payload["reasoning"].(map[string]interface{})
			if effort["effort"] != "low" {
				t.Errorf("reasoning = %v", effort)
			}
			text, _ := payload["text"].(map[string]interface{})
			if text["verbosity"] != "low" {
				t.Errorf("text = %v", text)
			}
			if stateful {
				if payload["previous_response_id"] != "resp_previous" {
					t.Errorf("previous_response_id = %v", payload["previous_response_id"])
				}
			} else {
				if _, ok := payload["previous_response_id"]; ok {
					t.Error("stateless request links a stored response")
				}
				include, _ := payload["include"].([]interface{})
				if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
					t.Errorf("include = %v", include)
				}
			}
			if headers.Get("Authorization") != "Bearer test-key" || headers.Get("ChatGPT-Account-Id") != "" || headers.Get("Originator") != "" || strings.Contains(headers.Get("User-Agent"), "codex") {
				t.Errorf("unexpected public API headers: %v", headers)
			}
			if resp.Content != "ok" || resp.ResponseID != "resp_test" || resp.Usage.InputTokens != 2048 || resp.Usage.CachedInputTokens != 1024 || resp.Usage.OutputTokens != 100 || resp.Usage.ReasoningTokens != 80 {
				t.Errorf("response = %+v", resp)
			}
		})
	}
}
