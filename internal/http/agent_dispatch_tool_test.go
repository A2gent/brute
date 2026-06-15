package http

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmptyDockerDelegationMessageIncludesToolLoopDiagnostics(t *testing.T) {
	message := emptyDockerDelegationMessage("agent-youtube", "child-1", ChatResponse{
		Status: "completed",
		Messages: []MessageResponse{
			{Role: "assistant", ToolCalls: []ToolCallResponse{{ID: "call-1", Name: "youtube_transcript", Input: json.RawMessage(`{"url":"https://youtu.be/abc"}`)}}},
			{Role: "tool", ToolResults: []ToolResultResponse{{ToolCallID: "call-1", Name: "youtube_transcript", Content: strings.Repeat("x", 15185)}}},
		},
	})

	for _, expected := range []string{
		`docker agent "agent-youtube" returned empty response`,
		"child session child-1",
		"status=completed",
		"1 tool call(s)",
		"1 tool result(s)",
		"last_tool=youtube_transcript",
		"last_tool_result_chars=15185",
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("expected diagnostic to contain %q, got %q", expected, message)
		}
	}
}
