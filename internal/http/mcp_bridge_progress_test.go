package http

import (
	"encoding/json"
	"testing"
)

func TestResolveLiveRuntimeToolCall(t *testing.T) {
	s := &Server{}
	inputCompleted := func(id, name, input string) ChatStreamEvent {
		return ChatStreamEvent{Type: "tool_input_completed", TurnID: "turn-1", RuntimeTool: &StreamRuntimeToolEvent{ID: id, Name: name, InputJSON: input}}
	}
	s.bufferLiveTurnEvent("sess", inputCompleted("toolu_done", "delegate_to_agent", `{"agent_id":"a","task":"x"}`))
	s.bufferLiveTurnEvent("sess", ChatStreamEvent{Type: "tool_output", TurnID: "turn-1", ToolResult: &StreamToolResultEvent{ToolCallID: "toolu_done"}})
	s.bufferLiveTurnEvent("sess", inputCompleted("toolu_a", "delegate_to_agent", `{"agent_id":"a","task":"x"}`))
	s.bufferLiveTurnEvent("sess", inputCompleted("toolu_b", "delegate_to_agent", `{"task":"y","agent_id":"b"}`))
	s.bufferLiveTurnEvent("sess", inputCompleted("toolu_read", "read", `{"path":"x"}`))

	t.Run("matches parallel calls by input regardless of key order", func(t *testing.T) {
		id, turn := s.resolveLiveRuntimeToolCall("sess", "delegate_to_agent", json.RawMessage(`{"agent_id":"b","task":"y"}`))
		if id != "toolu_b" || turn != "turn-1" {
			t.Fatalf("got %q %q", id, turn)
		}
		if id, _ := s.resolveLiveRuntimeToolCall("sess", "delegate_to_agent", json.RawMessage(`{"agent_id":"a","task":"x"}`)); id != "toolu_a" {
			t.Fatalf("finished call must be skipped, got %q", id)
		}
	})

	t.Run("ambiguous input yields no id", func(t *testing.T) {
		if id, _ := s.resolveLiveRuntimeToolCall("sess", "delegate_to_agent", json.RawMessage(`{"agent_id":"c"}`)); id != "" {
			t.Fatalf("expected no match, got %q", id)
		}
	})

	t.Run("single candidate is used when input differs", func(t *testing.T) {
		if id, _ := s.resolveLiveRuntimeToolCall("sess", "read", json.RawMessage(`{"path":"/workspace/x"}`)); id != "toolu_read" {
			t.Fatalf("got %q", id)
		}
	})
}
