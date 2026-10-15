package providerstest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
)

// CheckOpenAIParentProxy verifies credential-free child wiring at each entry point.
func CheckOpenAIParentProxy(t *testing.T, factory func(*config.Config) (llm.Client, error)) {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/providers/openai/chat/completions" || r.Header.Get("Authorization") != "Bearer proxy-token" {
			t.Errorf("proxy request = %s, auth = %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for key, want := range map[string]interface{}{
			"session_id": "child", "prompt_cache_key": "child", "reasoning_effort": "low",
			"text_verbosity": "low", "service_tier": "auto",
		} {
			if body[key] != want {
				t.Errorf("%s = %v, want %v", key, body[key], want)
			}
		}
		if _, capped := body["max_tokens"]; capped {
			t.Error("child added an implicit max_tokens cap")
		}
		if _, capped := body["max_completion_tokens"]; capped {
			t.Error("child added an implicit max_completion_tokens cap")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok","reasoning_items":[{"type":"reasoning","encrypted_content":"opaque"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":2048,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":1024},"completion_tokens_details":{"reasoning_tokens":80}}}`)
	}))
	defer proxy.Close()
	t.Setenv("A2GENT_PARENT_PROXY_URL", proxy.URL+"/v1/")
	t.Setenv("A2GENT_PARENT_PROXY_KEY", "proxy-token")
	t.Setenv("OPENAI_API_KEY", "")
	cfg := config.DefaultConfig()
	cfg.ActiveProvider = "openai"
	cfg.Providers["openai"] = config.Provider{Model: "gpt-5.4"}
	client, err := factory(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Chat(t.Context(), &llm.ChatRequest{
		SessionID: "child", PromptCacheKey: "parent-cache", ReasoningEffort: "low", TextVerbosity: "low", ServiceTier: "auto",
		Messages: []llm.Message{{Role: "user", Content: "start"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantUsage := llm.TokenUsage{InputTokens: 2048, OutputTokens: 100, CachedInputTokens: 1024, ReasoningTokens: 80}
	if result.Content != "ok" || len(result.ReasoningItems) != 1 || result.Usage != wantUsage {
		t.Errorf("proxy result lost content/state/usage: %+v", result)
	}
}
