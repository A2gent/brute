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

	// A latest reply without a turn must not borrow the previous turn's id.
	resp.Messages[2].Metadata = nil
	if got := directAgentReplyMetadata(resp); got != nil {
		t.Fatalf("metadata = %v, want nil", got)
	}
}
