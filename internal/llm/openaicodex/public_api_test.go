package openaicodex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestPublicAPIReplaysEncryptedReasoningAcrossToolTurns(t *testing.T) {
	const reasoning = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"}`
	var requests []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		requests = append(requests, body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_1","status":"completed","output":[`+reasoning+`,{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":2048,"input_tokens_details":{"cached_tokens":1024},"output_tokens":100,"output_tokens_details":{"reasoning_tokens":80}}}`)
	}))
	defer server.Close()
	client := NewClientWithOptions("key", "gpt-5.4-mini", server.URL+"/v1/chat/completions", Options{PublicAPI: true})
	req := &llm.ChatRequest{SessionID: "session-cache", Messages: []llm.Message{{Role: "user", Content: "lookup"}}}
	resp, err := client.Chat(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ReasoningItems) != 1 || resp.Usage.CachedInputTokens != 1024 || resp.Usage.ReasoningTokens != 80 {
		t.Fatalf("response = %+v", resp)
	}
	req.Messages = append(req.Messages, llm.Message{Role: "assistant", ToolCalls: resp.ToolCalls, ReasoningItems: resp.ReasoningItems}, llm.Message{Role: "tool", ToolResults: []llm.ToolResult{{ToolCallID: "call_1", Content: "value"}}})
	if _, err = client.Chat(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	input := requests[1]["input"].([]interface{})
	var want map[string]interface{}
	_ = json.Unmarshal([]byte(reasoning), &want)
	if !reflect.DeepEqual(input[1], want) {
		t.Errorf("reasoning replay = %v, want %v", input[1], want)
	}
	if input[2].(map[string]interface{})["type"] != "function_call" || input[3].(map[string]interface{})["type"] != "function_call_output" {
		t.Errorf("tool turn ordering = %v", input)
	}
	if requests[1]["prompt_cache_key"] != "session-cache" {
		t.Errorf("session cache key = %v", requests[1]["prompt_cache_key"])
	}
}

func TestPublicAPIRequestOverridesAndNoCodexAccountHeaders(t *testing.T) {
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"chatgpt_account_id":"account"}`)) + ".signature"
	for _, public := range []bool{false, true} {
		t.Run(fmt.Sprintf("public=%v", public), func(t *testing.T) {
			var body map[string]interface{}
			var headers http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headers = r.Header.Clone()
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"resp_1","status":"completed","output":[]}`)
			}))
			defer server.Close()
			c := NewClientWithOptions(token, "gpt-5.4", server.URL, Options{PublicAPI: public, StatefulResponses: true, MaxTokens: 128, ReasoningEffort: "low"})
			_, err := c.Chat(t.Context(), &llm.ChatRequest{SystemPrompt: "custom", MaxTokens: 256, ReasoningEffort: "high", TextVerbosity: "low", ServiceTier: "auto", PromptCacheKey: "override", PreviousResponseID: "resp_prev"})
			if err != nil {
				t.Fatal(err)
			}
			if body["reasoning"].(map[string]interface{})["effort"] != "high" || body["text"].(map[string]interface{})["verbosity"] != "low" || body["prompt_cache_key"] != "override" {
				t.Errorf("overrides = %v", body)
			}
			if public {
				if headers.Get("ChatGPT-Account-Id") != "" || headers.Get("Originator") != "" || body["instructions"] != "custom" || body["max_output_tokens"] != float64(256) || body["store"] != true || body["previous_response_id"] != "resp_prev" || body["service_tier"] != "auto" {
					t.Errorf("public payload/headers = %v / %v", body, headers)
				}
			} else {
				if headers.Get("ChatGPT-Account-Id") != "account" || headers.Get("Originator") != "codex_cli_rs" || !strings.HasPrefix(body["instructions"].(string), codexPrefix) || body["store"] != false {
					t.Errorf("Codex payload/headers = %v / %v", body, headers)
				}
				for _, key := range []string{"max_output_tokens", "previous_response_id", "service_tier"} {
					if _, ok := body[key]; ok {
						t.Errorf("Codex sent %s", key)
					}
				}
			}
		})
	}
}

func TestResponsesStreamTerminalEvents(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		_, err := parseStreamResponse([]byte("data: " + `{"type":"response.failed","response":{"id":"resp_failed","status":"failed","error":{"message":"quota exceeded"}}}` + "\n\n"))
		if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
			t.Fatalf("failure = %v", err)
		}
	})
	t.Run("incomplete", func(t *testing.T) {
		resp, err := parseStreamResponse([]byte("data: " + `{"type":"response.incomplete","response":{"id":"resp_capped","status":"incomplete","output":[{"type":"message","content":[{"type":"output_text","text":" partial \n"}]}],"usage":{"input_tokens":12,"output_tokens":64,"output_tokens_details":{"reasoning_tokens":50}}}}` + "\n\n"))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StopReason != "incomplete" || resp.ResponseID != "resp_capped" || resp.Usage.ReasoningTokens != 50 || resp.Content != " partial \n" {
			t.Fatalf("capped response = %+v", resp)
		}
	})
}
