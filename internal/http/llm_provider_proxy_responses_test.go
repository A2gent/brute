package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/lmstudio"
)

func TestOpenAIParentProxyMultiToolRoundtrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, stored := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/stored=%v", stream, stored), func(t *testing.T) {
				t.Setenv("OPENAI_BASE_URL", "")
				t.Setenv("A2GENT_PARENT_PROXY_URL", "")
				requests := make(chan map[string]interface{}, 4)
				turn := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer parent-secret" {
						t.Errorf("upstream request = %s, auth = %q", r.URL.Path, r.Header.Get("Authorization"))
					}
					var payload map[string]interface{}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					requests <- payload
					turn++
					output := []interface{}{map[string]interface{}{
						"type": "reasoning", "id": fmt.Sprintf("rs_%d", turn),
						"summary": []interface{}{}, "encrypted_content": fmt.Sprintf("opaque_%d", turn),
					}}
					if turn <= 2 {
						output = append(output, map[string]interface{}{
							"type": "function_call", "id": fmt.Sprintf("fc_%d", turn),
							"call_id": fmt.Sprintf("call_%d", turn), "name": "lookup", "arguments": `{"key":"value"}`,
						})
					} else {
						output = append(output, map[string]interface{}{
							"type": "message", "content": []interface{}{map[string]interface{}{"type": "output_text", "text": "done"}},
						})
					}
					event := map[string]interface{}{"type": "response.completed", "response": map[string]interface{}{
						"id": fmt.Sprintf("resp_%d", turn), "status": "completed", "output": output,
						"usage": map[string]interface{}{
							"input_tokens": 2048, "output_tokens": 100,
							"input_tokens_details":  map[string]interface{}{"cached_tokens": 1024},
							"output_tokens_details": map[string]interface{}{"reasoning_tokens": 80},
						},
					}}
					body, _ := json.Marshal(event)
					w.Header().Set("Content-Type", "text/event-stream")
					if turn <= 2 {
						// Responses output_index counts the preceding reasoning item too.
						delta, _ := json.Marshal(map[string]interface{}{
							"type": "response.function_call_arguments.delta", "output_index": 1,
							"item_id": fmt.Sprintf("fc_%d", turn), "delta": `{"key":"value"}`,
						})
						fmt.Fprintf(w, "data: %s\n\n", delta)
					}
					fmt.Fprintf(w, "data: %s\n\n", body)
				}))
				defer upstream.Close()

				parent, cleanup := newRequestLoggingTestServer(t)
				defer cleanup()
				parent.config.LLMRetries = 0
				parent.config.Providers["openai"] = config.Provider{
					APIKey: "parent-secret", BaseURL: upstream.URL + "/v1", Model: "gpt-5.4",
					ReasoningEffort: "high", TextVerbosity: "high", ServiceTier: "default",
					PromptCacheKey: "parent-cache", StatefulResponses: &stored,
				}
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer child-proxy-token" {
						t.Errorf("child must only have proxy credentials: %q", r.Header.Get("Authorization"))
					}
					parent.router.ServeHTTP(w, r)
				}))
				defer proxy.Close()
				t.Setenv("A2GENT_PARENT_PROXY_URL", proxy.URL+"/v1")
				t.Setenv("A2GENT_PARENT_PROXY_KEY", "child-proxy-token")
				child := &Server{config: config.DefaultConfig()}
				client, err := child.createBaseLLMClientForSession(config.ProviderOpenAI, "gpt-5.4", nil)
				if err != nil {
					t.Fatal(err)
				}
				// Both roles share this test process; only the child uses the proxy URL.
				t.Setenv("A2GENT_PARENT_PROXY_URL", "")
				request := &llm.ChatRequest{
					SessionID: "child-session", PromptCacheKey: "must-not-share-parent-cache",
					ReasoningEffort: "low", TextVerbosity: "low", ServiceTier: "auto",
					SystemPrompt: "Use lookup twice", Messages: []llm.Message{{Role: "user", Content: "start"}},
					Tools: []llm.ToolDefinition{{Name: "lookup", InputSchema: map[string]interface{}{"type": "object"}}},
				}
				wantUsage := llm.TokenUsage{InputTokens: 2048, OutputTokens: 100, CachedInputTokens: 1024, ReasoningTokens: 80}
				for step := 1; step <= 3; step++ {
					var response *llm.ChatResponse
					var eventUsage llm.TokenUsage
					if stream {
						response, err = client.(llm.StreamingClient).ChatStream(t.Context(), request, func(event llm.StreamEvent) error {
							if event.Type == llm.StreamEventUsage {
								eventUsage = event.Usage
							}
							return nil
						})
					} else {
						response, err = client.Chat(t.Context(), request)
					}
					if err != nil {
						t.Fatal(err)
					}
					if response.Usage != wantUsage || (stream && eventUsage != wantUsage) {
						t.Errorf("usage = %+v, event usage = %+v, want %+v", response.Usage, eventUsage, wantUsage)
					}
					if len(response.ReasoningItems) != 1 {
						t.Fatalf("step %d reasoning items = %s", step, response.ReasoningItems)
					}
					payload := <-requests
					for key, want := range map[string]interface{}{"prompt_cache_key": "child-session", "service_tier": "auto", "store": stored} {
						if payload[key] != want {
							t.Errorf("%s = %v, want %v", key, payload[key], want)
						}
					}
					if !reflect.DeepEqual(payload["reasoning"], map[string]interface{}{"effort": "low"}) || !reflect.DeepEqual(payload["text"], map[string]interface{}{"verbosity": "low"}) {
						t.Errorf("request options lost: %v", payload)
					}
					if step == 1 {
						if _, capped := payload["max_output_tokens"]; capped {
							t.Errorf("proxy added an output cap: %v", payload["max_output_tokens"])
						}
					} else if payload["max_output_tokens"] != float64(8192) {
						t.Errorf("explicit token cap = %v, want 8192", payload["max_output_tokens"])
					}
					if !reflect.DeepEqual(payload["include"], []interface{}{"reasoning.encrypted_content"}) {
						t.Errorf("encrypted reasoning not requested: %v", payload["include"])
					}
					input, _ := payload["input"].([]interface{})
					for previous := 1; previous < step; previous++ {
						var found bool
						for i, raw := range input {
							item := raw.(map[string]interface{})
							if item["type"] == "reasoning" && item["id"] == fmt.Sprintf("rs_%d", previous) {
								found = item["encrypted_content"] == fmt.Sprintf("opaque_%d", previous)
								if i+2 >= len(input) || input[i+1].(map[string]interface{})["call_id"] != fmt.Sprintf("call_%d", previous) || input[i+2].(map[string]interface{})["type"] != "function_call_output" {
									t.Errorf("reasoning/tool ordering lost: %v", input)
								}
							}
						}
						if !found {
							t.Errorf("upstream step %d is missing reasoning from step %d: %v", step, previous, input)
						}
					}
					if step == 3 {
						if response.Content != "done" || len(response.ToolCalls) != 0 {
							t.Errorf("final response = %+v", response)
						}
						break
					}
					if len(response.ToolCalls) != 1 || response.ToolCalls[0].Input != `{"key":"value"}` {
						t.Fatalf("step %d tool calls = %+v", step, response.ToolCalls)
					}
					request.Messages = append(request.Messages,
						llm.Message{Role: "assistant", ToolCalls: response.ToolCalls, ReasoningItems: response.ReasoningItems},
						llm.Message{Role: "tool", ToolResults: []llm.ToolResult{{ToolCallID: response.ToolCalls[0].ID, Content: "tool result"}}},
					)
					request.MaxTokens = 8192
				}
			})
		}
	}
}

type proxyChatOnlyClient struct{ response *llm.ChatResponse }

func (c proxyChatOnlyClient) Chat(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
	return c.response, nil
}

func TestParentProxyStreamingChatOnlyFallbackPreservesStateAndUsage(t *testing.T) {
	response := &llm.ChatResponse{
		Content: "ok", ReasoningItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"rs_fallback","encrypted_content":"opaque"}`)},
		Usage: llm.TokenUsage{InputTokens: 2048, OutputTokens: 100, CachedInputTokens: 1024, ReasoningTokens: 80},
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(&Server{}).handleLLMProxyChatCompletionsStream(w, r, proxyChatOnlyClient{response}, &llm.ChatRequest{}, "gpt-5.4")
	}))
	defer proxy.Close()
	client := lmstudio.NewClient("proxy-token", "gpt-5.4", proxy.URL)
	result, err := client.ChatStream(t.Context(), &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "start"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage != response.Usage || !reflect.DeepEqual(result.ReasoningItems, response.ReasoningItems) {
		t.Errorf("fallback lost state or usage: %+v", result)
	}
}

func TestBuildProxyLLMRequestPreservesOptions(t *testing.T) {
	legacyCap, completionCap := 4096, 8192
	request, err := buildProxyLLMRequest(&proxyChatRequest{
		SessionID: " child ", PromptCacheKey: "parent", ReasoningEffort: " low ", TextVerbosity: " low ", ServiceTier: " auto ",
		MaxTokens: &legacyCap, MaxCompletionTokens: &completionCap,
	}, "gpt-5.4")
	if err != nil {
		t.Fatal(err)
	}
	if request.SessionID != "child" || request.PromptCacheKey != "child" || request.MaxTokens != completionCap || request.ReasoningEffort != "low" || request.TextVerbosity != "low" || request.ServiceTier != "auto" {
		t.Errorf("options lost: %+v", request)
	}
	request, err = buildProxyLLMRequest(&proxyChatRequest{PromptCacheKey: " explicit "}, "gpt-5.4")
	if err != nil || request.PromptCacheKey != "explicit" {
		t.Errorf("explicit cache key without a session: %+v, %v", request, err)
	}
}
