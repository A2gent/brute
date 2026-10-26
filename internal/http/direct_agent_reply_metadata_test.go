package http

import "testing"

func TestDirectAgentReplyMetadataUsesLatestReplyTurn(t *testing.T) {
	resp := ChatResponse{Messages: []MessageResponse{
		{Role: "assistant", Metadata: map[string]interface{}{"runtime_turn_id": "old"}},
		{Role: "user"},
		{Role: "assistant", Metadata: map[string]interface{}{"runtime_turn_id": "new"}},
	}}
	if got := directAgentReplyMetadata(resp)["runtime_turn_id"]; got != "new" {
		t.Fatalf("runtime_turn_id = %v, want new", got)
	}

	// Tool activity of the child turn must survive into the parent copy.
	resp.Messages[2].Metadata = map[string]interface{}{"runtime_turn_id": "new", "runtime_tools": map[string]interface{}{"t1": 1}, "llm_usage": 1}
	got := directAgentReplyMetadata(resp)
	if got["runtime_tools"] == nil || got["llm_usage"] != nil {
		t.Fatalf("metadata = %v, want runtime_* keys only", got)
	}

	// A latest reply without a turn must not borrow the previous turn's id.
	resp.Messages[2].Metadata = nil
	if got := directAgentReplyMetadata(resp); got != nil {
		t.Fatalf("metadata = %v, want nil", got)
	}
}
