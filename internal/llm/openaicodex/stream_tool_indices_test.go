package openaicodex

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestStreamToolIndicesExcludeReasoningItems(t *testing.T) {
	stream := `data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"{}"}

data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}}

data: {"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"lookup","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":3,"item_id":"fc_2","delta":"{}"}

data: {"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"lookup","arguments":"{}"}}

`
	var events []llm.StreamEvent
	result, err := parseStreamReader(strings.NewReader(stream), func(event llm.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || len(result.ToolCalls) != 2 {
		t.Fatalf("events = %+v, result = %+v", events, result)
	}
	for i, event := range events {
		call := result.ToolCalls[i]
		if event.ToolCallIndex != i || event.ToolCallID != call.ID || event.ToolCallName != call.Name || event.ToolInputDelta != call.Input {
			t.Errorf("event %d does not match final call: %+v / %+v", i, event, call)
		}
	}
}
