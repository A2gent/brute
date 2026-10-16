package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestPromptCachingChatAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				cache := map[string]interface{}{"type": "ephemeral"}
				system := body["system"].([]interface{})
				if len(system) != 1 || !reflect.DeepEqual(system[0].(map[string]interface{})["cache_control"], cache) {
					t.Errorf("system cache breakpoint missing: %v", system)
				}
				tools := body["tools"].([]interface{})
				if tools[0].(map[string]interface{})["cache_control"] != nil || !reflect.DeepEqual(tools[1].(map[string]interface{})["cache_control"], cache) {
					t.Errorf("tools cache breakpoint is not on the last tool: %v", tools)
				}
				messages := body["messages"].([]interface{})
				blocks := messages[len(messages)-1].(map[string]interface{})["content"].([]interface{})
				last := blocks[len(blocks)-1].(map[string]interface{})
				if last["type"] != "image" || !reflect.DeepEqual(last["cache_control"], cache) {
					t.Errorf("image is outside the cached prefix: %v", blocks)
				}
				usage := `{"input_tokens":12,"output_tokens":3,"cache_creation_input_tokens":100,"cache_read_input_tokens":900}`
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":%s}}\n\n", usage)
					fmt.Fprint(w, "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
					fmt.Fprint(w, "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {}\n\n")
				} else {
					fmt.Fprintf(w, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":%s}`, usage)
				}
			}))
			defer server.Close()
			client := NewClientWithBaseURL("test", "claude-test", server.URL)
			request := &llm.ChatRequest{
				SystemPrompt: "stable instructions",
				Tools: []llm.ToolDefinition{
					{Name: "first", InputSchema: map[string]interface{}{"type": "object"}},
					{Name: "last", InputSchema: map[string]interface{}{"type": "object"}},
				},
				Messages: []llm.Message{{Role: "user", Content: "inspect", Images: []llm.Image{{MediaType: "image/png", DataBase64: "cG5n"}}}},
			}
			var response *llm.ChatResponse
			var err error
			var events []llm.TokenUsage
			if stream {
				response, err = client.ChatStream(context.Background(), request, func(event llm.StreamEvent) error {
					if event.Type == llm.StreamEventUsage {
						events = append(events, event.Usage)
					}
					return nil
				})
			} else {
				response, err = client.Chat(context.Background(), request)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := llm.TokenUsage{InputTokens: 1012, OutputTokens: 3, CachedInputTokens: 900}
			if stream {
				want.OutputTokens = 5
				if len(events) != 2 || events[0].CachedInputTokens != 900 || events[1] != want {
					t.Fatalf("stream cache usage events = %+v", events)
				}
			}
			if response.Usage != want || response.Content != "ok" {
				t.Fatalf("response = %+v, want usage %+v", response, want)
			}
		})
	}
}

func TestPromptCachingMessageBlocks(t *testing.T) {
	for _, content := range []interface{}{
		"stable text",
		[]contentBlock{{Type: "tool_use", ID: "call", Name: "read", Input: map[string]interface{}{"path": "screenshot.png"}}},
		[]contentBlock{{Type: "tool_result", ToolUseID: "call", Content: "result"}},
	} {
		t.Run(fmt.Sprintf("%T/%v", content, content), func(t *testing.T) {
			request := anthropicRequest{Messages: []anthropicMessage{{Role: "user", Content: content}, {Role: "user", Content: ""}}}
			NewClient("", "").WithClaudeCodeMode(false).applyPromptCaching(&request, "")
			if len(request.Tools) != 0 || len(request.System) != 0 {
				t.Fatal("caching added absent tools or system prompt")
			}
			blocks := request.Messages[0].Content.([]contentBlock)
			if len(blocks) != 1 || blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != "ephemeral" {
				t.Fatalf("last stable content has no cache breakpoint: %+v", blocks)
			}
		})
	}
}
