package lmstudio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestChatJSONSchemaSendsResponseFormatAndReadsReasoningContent(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"","reasoning_content":"{\"ok\":1}"}}]}`))
	}))
	defer server.Close()

	resp, err := NewClient("", "m", server.URL).Chat(context.Background(), &llm.ChatRequest{
		Messages:   []llm.Message{{Role: "user", Content: "hi"}},
		JSONSchema: map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatal(err)
	}
	format, _ := body["response_format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Fatalf("response_format = %#v", body["response_format"])
	}
	if resp.Content != `{"ok":1}` {
		t.Fatalf("content = %q", resp.Content)
	}
}
