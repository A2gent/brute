package openaicodex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestBuildInputItems_IncludesEmptyFunctionCallOutput(t *testing.T) {
	items := buildInputItems([]llm.Message{
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "call_empty", Name: "bash", Input: `{"command":"true"}`},
			},
		},
		{
			Role: "tool",
			ToolResults: []llm.ToolResult{
				{ToolCallID: "call_empty", Name: "bash", Content: ""},
			},
		},
	})

	if len(items) != 2 {
		t.Fatalf("item count = %d, want 2", len(items))
	}
	if items[1].Output == nil {
		t.Fatalf("function_call_output output was omitted")
	}
	if got := *items[1].Output; got != "" {
		t.Fatalf("function_call_output output = %q, want empty string", got)
	}

	body, err := json.Marshal(items[1])
	if err != nil {
		t.Fatalf("marshal function_call_output: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal function_call_output: %v", err)
	}
	if _, ok := raw["output"]; !ok {
		t.Fatalf("marshaled function_call_output missing output field: %s", string(body))
	}
}

func TestBuildInputItems_DoesNotAddOutputToMessages(t *testing.T) {
	items := buildInputItems([]llm.Message{
		{Role: "user", Content: "hello"},
	})

	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	body, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal message item: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal message item: %v", err)
	}
	if _, ok := raw["output"]; ok {
		t.Fatalf("message item unexpectedly includes output field: %s", string(body))
	}
}

func TestRequestOptions_DisablesStatefulResponses(t *testing.T) {
	client := NewClientWithOptions("", "gpt-5.5", "", Options{
		StatefulResponses: true,
	})

	options := client.requestOptions(&llm.ChatRequest{
		Store:              true,
		PreviousResponseID: "resp_123",
	})

	if options.StatefulResponses {
		t.Fatalf("stateful responses enabled for OpenAI Codex")
	}
	if got := options.previousResponseID(&llm.ChatRequest{PreviousResponseID: "resp_123"}); got != "" {
		t.Fatalf("previous response id = %q, want empty", got)
	}
}

func TestChat_DoesNotSendUnsupportedMaxOutputTokens(t *testing.T) {
	var raw map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/responses"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := raw["max_output_tokens"]; ok {
			t.Fatalf("request included unsupported max_output_tokens: %v", raw["max_output_tokens"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}

`))
	}))
	defer server.Close()

	client := NewClientWithOptions("token", "gpt-5.5", server.URL, Options{MaxTokens: 200})
	client.httpClient = server.Client()

	resp, err := client.Chat(t.Context(), &llm.ChatRequest{
		Messages:  []llm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if got, want := resp.Content, "ok"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestParseStreamResponse_UsesStreamedMessageWhenCompletedOutputIsEmpty(t *testing.T) {
	body := []byte(`event: response.output_item.added
data: {"type":"response.output_item.added","item":{"id":"msg_1","type":"message","status":"in_progress","content":[],"phase":"final_answer","role":"assistant"},"output_index":0,"sequence_number":2}

event: response.content_part.added
data: {"type":"response.content_part.added","content_index":0,"item_id":"msg_1","output_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""},"sequence_number":3}

event: response.output_text.delta
data: {"type":"response.output_text.delta","content_index":0,"delta":"Hello","item_id":"msg_1","output_index":0,"sequence_number":4}

event: response.output_text.delta
data: {"type":"response.output_text.delta","content_index":0,"delta":" world","item_id":"msg_1","output_index":0,"sequence_number":5}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","text":"Hello world"}],"phase":"final_answer","role":"assistant"},"output_index":0,"sequence_number":6}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":4,"output_tokens":2}},"sequence_number":7}
`)

	resp, err := parseStreamResponse(body)
	if err != nil {
		t.Fatalf("parseStreamResponse returned error: %v", err)
	}
	if got, want := resp.Content, "Hello world"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if got, want := resp.Usage.InputTokens, 4; got != want {
		t.Fatalf("input tokens = %d, want %d", got, want)
	}
	if got, want := resp.Usage.OutputTokens, 2; got != want {
		t.Fatalf("output tokens = %d, want %d", got, want)
	}
}

func TestParseStreamResponse_PreservesToolCallsWhenCompletedOutputIsEmpty(t *testing.T) {
	body := []byte(`event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","delta":"{\"path\":\"README.md\"}","item_id":"fc_1","output_index":0,"sequence_number":1}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"},"output_index":0,"sequence_number":2}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":1}},"sequence_number":3}
`)

	resp, err := parseStreamResponse(body)
	if err != nil {
		t.Fatalf("parseStreamResponse returned error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1", len(resp.ToolCalls))
	}
	if got, want := resp.ToolCalls[0].ID, "call_1"; got != want {
		t.Fatalf("tool call id = %q, want %q", got, want)
	}
	if got, want := resp.ToolCalls[0].Name, "read_file"; got != want {
		t.Fatalf("tool call name = %q, want %q", got, want)
	}
	if got, want := resp.ToolCalls[0].Input, "{\"path\":\"README.md\"}"; got != want {
		t.Fatalf("tool call input = %q, want %q", got, want)
	}
}
