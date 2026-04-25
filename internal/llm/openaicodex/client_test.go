package openaicodex

import "testing"

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
