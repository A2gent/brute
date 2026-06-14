package openaicodex

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestBuildInputItems_PreservesExactToolResultOutput(t *testing.T) {
	content := "line one\nline two\n"
	items := buildInputItems([]llm.Message{
		{
			Role:      "assistant",
			ToolCalls: []llm.ToolCall{{ID: "call_read", Name: "read", Input: `{"path":"file.txt"}`}},
		},
		{
			Role:        "tool",
			ToolResults: []llm.ToolResult{{ToolCallID: "call_read", Name: "read", Content: content}},
		},
	})

	if len(items) != 2 {
		t.Fatalf("item count = %d, want 2", len(items))
	}
	if items[1].Output == nil {
		t.Fatalf("function_call_output output was omitted")
	}
	if got := *items[1].Output; got != content {
		t.Fatalf("function_call_output output = %q, want %q", got, content)
	}
	if strings.HasSuffix(*items[1].Output, "\n") == false {
		t.Fatalf("expected exact trailing newline to be preserved")
	}
}
