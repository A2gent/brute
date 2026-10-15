package lmstudio

import (
	"encoding/json"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestParentProxyExtensionsAreOptIn(t *testing.T) {
	for _, mode := range []string{"ordinary", "proxy"} {
		proxy := mode == "proxy"
		for _, provider := range []string{"openai", "anthropic", "lmstudio", "openrouter", "google", "openai_codex"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				baseURL := "http://parent/v1/providers/" + provider
				client := NewClient("token", "gpt-5.4", baseURL)
				if proxy {
					client = NewParentProxyClient("token", "gpt-5.4", baseURL)
				}
				request := &llm.ChatRequest{
					SessionID: "child", PromptCacheKey: "parent", ReasoningEffort: "low", TextVerbosity: "low", ServiceTier: "auto",
				}
				body := openAIRequest{}
				applyTokenLimit(&body, "gpt-5.4", client.effectiveMaxTokens(0), 0)
				client.applyRequestOptions(&body, request)
				if proxy && provider == "openai" {
					if body.MaxTokens != 0 || body.MaxCompletionTokens != 0 {
						t.Errorf("OpenAI proxy has implicit cap: %+v", body)
					}
				} else if body.MaxCompletionTokens != defaultMaxTokens {
					t.Errorf("legacy token behavior changed: %+v", body)
				}
				if proxy {
					if body.SessionID != "child" || body.PromptCacheKey != "child" || body.ReasoningEffort != "low" || body.TextVerbosity != "low" || body.ServiceTier != "auto" {
						t.Errorf("proxy options lost: %+v", body)
					}
				} else if body.SessionID != "" || body.PromptCacheKey != "" || body.ReasoningEffort != "" || body.TextVerbosity != "" || body.ServiceTier != "" {
					t.Errorf("private options leaked to ordinary endpoint: %+v", body)
				}
				for _, toolCall := range []bool{false, true} {
					message := llm.Message{Role: "assistant", Content: "text", ReasoningItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}}
					if toolCall {
						message.ToolCalls = []llm.ToolCall{{ID: "call_1", Name: "lookup", Input: `{}`}}
					}
					converted := client.convertMessage(message)
					if (len(converted[0].ReasoningItems) != 0) != proxy {
						t.Errorf("reasoning opt-in not respected: %+v", converted)
					}
				}
			})
		}
	}
}
