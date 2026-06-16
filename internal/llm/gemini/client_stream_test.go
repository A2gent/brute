package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestChatStreamSeparatesToolCallsWhenGeminiOmitsIndex(t *testing.T) {
	server := newGeminiStreamServer(t,
		geminiStreamChunk(t, []map[string]interface{}{
			geminiStreamToolCall("call_progress", "session_task_progress", `{"action":"set"`),
		}),
		geminiStreamChunk(t, []map[string]interface{}{
			geminiStreamToolCall("call_progress", "", `,"content":"one"}`),
		}),
		geminiStreamChunk(t, []map[string]interface{}{
			geminiStreamToolCall("call_find", "find_files", `{"pattern":"**/*"`),
		}),
		geminiStreamChunk(t, []map[string]interface{}{
			geminiStreamToolCall("call_find", "", `,"path":"/workspace"}`),
		}),
	)
	defer server.Close()

	client := NewClient("", "gemini-test", server.URL)
	resp, err := client.ChatStream(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "inspect"}},
	}, nil)
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool call count = %d, want 2: %#v", len(resp.ToolCalls), resp.ToolCalls)
	}

	first := resp.ToolCalls[0]
	if first.ID != "call_progress" || first.Name != "session_task_progress" || first.Input != `{"action":"set","content":"one"}` {
		t.Fatalf("first tool call mismatch: %#v", first)
	}
	if !json.Valid([]byte(first.Input)) {
		t.Fatalf("first tool input is not valid JSON: %q", first.Input)
	}

	second := resp.ToolCalls[1]
	if second.ID != "call_find" || second.Name != "find_files" || second.Input != `{"pattern":"**/*","path":"/workspace"}` {
		t.Fatalf("second tool call mismatch: %#v", second)
	}
	if !json.Valid([]byte(second.Input)) {
		t.Fatalf("second tool input is not valid JSON: %q", second.Input)
	}
}

func TestChatStreamDoesNotDuplicateGeminiThoughtSignatureSnapshots(t *testing.T) {
	const signature = "gemini-signature"
	server := newGeminiStreamServer(t,
		geminiStreamChunk(t, []map[string]interface{}{
			geminiStreamToolCallWithSignature("call_read", "read", `{"path":"README.md"`, signature),
		}),
		geminiStreamChunk(t, []map[string]interface{}{
			geminiStreamToolCallWithSignature("call_read", "", `}`, signature),
		}),
	)
	defer server.Close()

	client := NewClient("", "gemini-test", server.URL)
	resp, err := client.ChatStream(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "read"}},
	}, nil)
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].ThoughtSignature; got != signature {
		t.Fatalf("thought signature = %q, want %q", got, signature)
	}
}

func newGeminiStreamServer(t *testing.T, chunks ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func geminiStreamChunk(t *testing.T, toolCalls []map[string]interface{}) string {
	t.Helper()
	payload := map[string]interface{}{
		"choices": []map[string]interface{}{
			{
				"delta": map[string]interface{}{
					"tool_calls": toolCalls,
				},
			},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal stream chunk: %v", err)
	}
	return string(encoded)
}

func geminiStreamToolCall(id string, name string, arguments string) map[string]interface{} {
	return geminiStreamToolCallWithSignature(id, name, arguments, "")
}

func geminiStreamToolCallWithSignature(id string, name string, arguments string, signature string) map[string]interface{} {
	function := map[string]interface{}{
		"arguments": arguments,
	}
	if name != "" {
		function["name"] = name
	}
	if signature != "" {
		function["thought_signature"] = signature
	}
	call := map[string]interface{}{
		"id":       id,
		"type":     "function",
		"function": function,
	}
	if signature != "" {
		call["extra_content"] = map[string]interface{}{
			"google": map[string]interface{}{
				"thought_signature": signature,
			},
		}
	}
	return call
}
