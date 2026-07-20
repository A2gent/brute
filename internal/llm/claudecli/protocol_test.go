package claudecli

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestToolResultContentString(t *testing.T) {
	content, invalid := toolResultContent(json.RawMessage(`"hello world"`))
	if invalid || content != "hello world" {
		t.Fatalf("string content = %q invalid=%v, want hello world", content, invalid)
	}
}

func TestToolResultContentTextBlocks(t *testing.T) {
	raw := json.RawMessage(`[{"type":"text","text":"line1"},{"type":"text","text":"line2"}]`)
	content, invalid := toolResultContent(raw)
	if invalid || content != "line1line2" {
		t.Fatalf("array content = %q invalid=%v", content, invalid)
	}
}

func TestToolResultContentInvalidJSON(t *testing.T) {
	_, invalid := toolResultContent(json.RawMessage(`not-json`))
	if !invalid {
		t.Fatal("expected invalid tool result content")
	}
}

func TestToolResultContentUnsupportedObject(t *testing.T) {
	content, invalid := toolResultContent(json.RawMessage(`{"unexpected":"shape"}`))
	if invalid {
		t.Fatal("unsupported object should not be treated as invalid JSON")
	}
	if content != `{"unexpected":"shape"}` {
		t.Fatalf("content = %q, want compact unsupported JSON", content)
	}
}

func TestToolResultContentMixedNonTextArray(t *testing.T) {
	raw := json.RawMessage(`[{"type":"text","text":"x"},{"type":"image","source":{"type":"base64"}}]`)
	content, invalid := toolResultContent(raw)
	if invalid {
		t.Fatal("mixed non-text array should not be treated as invalid JSON")
	}
	if content != string(raw) {
		t.Fatalf("content = %q, want compact mixed array JSON", content)
	}
}

func TestStreamProcessorContentBlockStopEmitsToolInputCompleted(t *testing.T) {
	p := newStreamProcessor(nil)
	p.blocksByIndex[2] = &toolBlockState{
		index: 2, id: "toolu_1", name: "Read", started: true,
	}
	p.blocksByIndex[2].input.WriteString(`{"file_path":"foo.go"}`)
	p.toolsByID["toolu_1"] = p.blocksByIndex[2]

	var out llm.StreamEvent
	p.onEvent = func(ev llm.StreamEvent) error {
		out = ev
		return nil
	}

	if err := p.handleContentBlockStop(cliStreamEvent{Index: 2}); err != nil {
		t.Fatalf("handleContentBlockStop: %v", err)
	}
	if out.Type != llm.StreamEventToolInputCompleted || out.ToolCallName != "Read" {
		t.Fatalf("got %+v, want tool_input_completed Read", out)
	}
	if out.ToolInputDelta != `{"file_path":"foo.go"}` {
		t.Fatalf("input = %q, want final JSON", out.ToolInputDelta)
	}
}

func TestStreamProcessorValidToolResultEmitsOutputThenCompleted(t *testing.T) {
	p := newStreamProcessor(nil)
	p.toolsByID["toolu_1"] = &toolBlockState{
		id: "toolu_1", name: "Read", index: 2,
	}
	p.toolsByID["toolu_1"].input.WriteString(`{"file_path":"foo.go"}`)

	var got []llm.StreamEvent
	p.onEvent = func(ev llm.StreamEvent) error {
		got = append(got, ev)
		return nil
	}

	if err := p.handleEnvelope(cliStreamEnvelope{
		Type: "user",
		Message: cliStreamMessage{
			Content: []cliStreamContent{{
				Type:      "tool_result",
				ToolUseID: "toolu_1",
				Content:   json.RawMessage(`"done"`),
			}},
		},
	}); err != nil {
		t.Fatalf("handleEnvelope: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want tool_output then tool_completed: %+v", len(got), got)
	}
	if got[0].Type != llm.StreamEventToolOutput || got[0].ToolOutput != "done" {
		t.Fatalf("first event = %+v, want tool_output", got[0])
	}
	if got[1].Type != llm.StreamEventToolCompleted || got[1].ToolCallName != "Read" ||
		got[1].ToolInputDelta != `{"file_path":"foo.go"}` {
		t.Fatalf("second event = %+v, want tool_completed with correlated input", got[1])
	}
}

func TestStreamProcessorMissingToolUseIDEmitsRuntimeWarning(t *testing.T) {
	var out llm.StreamEvent
	p := newStreamProcessor(func(ev llm.StreamEvent) error {
		out = ev
		return nil
	})

	if err := p.handleEnvelope(cliStreamEnvelope{
		Type: "user",
		Message: cliStreamMessage{
			Content: []cliStreamContent{{
				Type:    "tool_result",
				Content: json.RawMessage(`"orphan"`),
			}},
		},
	}); err != nil {
		t.Fatalf("handleEnvelope: %v", err)
	}
	if out.Type != llm.StreamEventRuntimeWarning || out.RuntimeStatus != "missing_tool_use_id" {
		t.Fatalf("got %+v, want runtime_warning missing_tool_use_id", out)
	}
}

func TestStreamProcessorInvalidToolResultContentEmitsRuntimeWarning(t *testing.T) {
	var out llm.StreamEvent
	p := newStreamProcessor(func(ev llm.StreamEvent) error {
		out = ev
		return nil
	})

	if err := p.handleEnvelope(cliStreamEnvelope{
		Type: "user",
		Message: cliStreamMessage{
			Content: []cliStreamContent{{
				Type:      "tool_result",
				ToolUseID: "toolu_1",
				Content:   json.RawMessage(`not-json`),
			}},
		},
	}); err != nil {
		t.Fatalf("handleEnvelope: %v", err)
	}
	if out.Type != llm.StreamEventRuntimeWarning || out.RuntimeStatus != "invalid_tool_result_content" {
		t.Fatalf("got %+v, want runtime_warning invalid_tool_result_content", out)
	}
}

func TestStreamProcessorUnsupportedToolResultShapeEmitsOutput(t *testing.T) {
	p := newStreamProcessor(nil)
	p.toolsByID["toolu_1"] = &toolBlockState{id: "toolu_1", name: "Read"}

	var got []llm.StreamEvent
	p.onEvent = func(ev llm.StreamEvent) error {
		got = append(got, ev)
		return nil
	}

	if err := p.handleEnvelope(cliStreamEnvelope{
		Type: "user",
		Message: cliStreamMessage{
			Content: []cliStreamContent{{
				Type:      "tool_result",
				ToolUseID: "toolu_1",
				Content:   json.RawMessage(`{"unexpected":"shape"}`),
				IsError:   true,
			}},
		},
	}); err != nil {
		t.Fatalf("handleEnvelope: %v", err)
	}
	if len(got) != 2 || got[0].Type != llm.StreamEventToolOutput || got[1].Type != llm.StreamEventToolCompleted {
		t.Fatalf("got %+v, want tool_output then tool_completed", got)
	}
	if got[0].ToolCallName != "Read" {
		t.Fatalf("tool_output name = %q, want Read", got[0].ToolCallName)
	}
	if got[0].ToolOutput != `{"unexpected":"shape"}` || !got[0].ToolIsError {
		t.Fatalf("got output=%q isError=%v, want compact JSON with provider is_error", got[0].ToolOutput, got[0].ToolIsError)
	}

}
func TestStreamProcessorNativeToolLifecycleOrder(t *testing.T) {
	var got []llm.StreamEvent
	p := newStreamProcessor(func(ev llm.StreamEvent) error {
		got = append(got, ev)
		return nil
	})

	steps := []cliStreamEnvelope{
		{
			Type: "stream_event",
			Event: cliStreamEvent{
				Type: "content_block_start", Index: 2,
				ContentBlock: cliStreamContentBlock{Type: "tool_use", ID: "toolu_1", Name: "Read"},
			},
		},
		{
			Type: "stream_event",
			Event: cliStreamEvent{
				Type: "content_block_delta", Index: 2,
				Delta: cliStreamDelta{Type: "input_json_delta", PartialJSON: `{"file_path":"foo.go"}`},
			},
		},
		{Type: "stream_event", Event: cliStreamEvent{Type: "content_block_stop", Index: 2}},
		{
			Type: "user",
			Message: cliStreamMessage{Content: []cliStreamContent{{
				Type: "tool_result", ToolUseID: "toolu_1", Content: json.RawMessage(`"package main"`),
			}}},
		},
	}
	for _, step := range steps {
		if err := p.handleEnvelope(step); err != nil {
			t.Fatalf("handleEnvelope: %v", err)
		}
	}

	want := []llm.StreamEventType{
		llm.StreamEventToolStarted,
		llm.StreamEventToolUpdated,
		llm.StreamEventToolInputCompleted,
		llm.StreamEventToolOutput,
		llm.StreamEventToolCompleted,
	}
	gotTypes := make([]llm.StreamEventType, len(got))
	for i, ev := range got {
		gotTypes[i] = ev.Type
	}
	if !slices.Equal(gotTypes, want) {
		t.Fatalf("events = %v, want %v", gotTypes, want)
	}
	if got[3].ToolOutput != "package main" || got[3].ToolIsError {
		t.Fatalf("tool_output = %+v, want successful package main output", got[3])
	}
	if got[4].ToolCallID != "toolu_1" || got[4].ToolCallName != "Read" || got[4].ToolInputDelta != `{"file_path":"foo.go"}` {
		t.Fatalf("tool_completed = %+v, want correlated id/name/input", got[4])
	}
}

func TestStreamProcessorSystemInitIsNotWarning(t *testing.T) {
	p := newStreamProcessor(func(ev llm.StreamEvent) error {
		t.Fatalf("unexpected event: %+v", ev)
		return nil
	})
	if err := p.handleEnvelope(cliStreamEnvelope{
		Type:    "system",
		Subtype: "init",
		Status:  "ready",
	}); err != nil {
		t.Fatalf("handleEnvelope: %v", err)
	}
}

func TestParseCLIStreamEnvelopeSystemStatusStringMessage(t *testing.T) {
	line := `{"type":"system","subtype":"status","status":"rate_limited","message":"Retrying after rate limit"}`
	event, err := parseCLIStreamEnvelope(line)
	if err != nil {
		t.Fatalf("parseCLIStreamEnvelope: %v", err)
	}
	if event.Type != "system" || event.Subtype != "status" || event.Status != "rate_limited" {
		t.Fatalf("envelope = %+v, want system/status/rate_limited", event)
	}
	if event.MessageText != "Retrying after rate limit" {
		t.Fatalf("MessageText = %q, want Retrying after rate limit", event.MessageText)
	}
	if len(event.Message.Content) != 0 {
		t.Fatalf("Message object should be empty for string message, got %+v", event.Message)
	}
}

func TestParseCLIStreamEnvelopeCompactBoundaryMetadata(t *testing.T) {
	line := `{"type":"system","subtype":"compact_boundary","compact_metadata":{"tokens_before":1200}}`
	event, err := parseCLIStreamEnvelope(line)
	if err != nil {
		t.Fatalf("parseCLIStreamEnvelope: %v", err)
	}
	if event.Type != "system" || event.Subtype != "compact_boundary" {
		t.Fatalf("envelope = %+v, want system/compact_boundary", event)
	}
	if event.Status != "" || event.MessageText != "" {
		t.Fatalf("status/message should be empty, got status=%q message=%q", event.Status, event.MessageText)
	}
	if len(event.CompactMetadata) == 0 {
		t.Fatal("expected compact_metadata to be preserved")
	}
}

func TestParseCLIStreamEnvelopeAssistantMessageObject(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`
	event, err := parseCLIStreamEnvelope(line)
	if err != nil {
		t.Fatalf("parseCLIStreamEnvelope: %v", err)
	}
	if event.MessageText != "" {
		t.Fatalf("MessageText = %q, want empty", event.MessageText)
	}
	if streamMessageText(event.Message) != "hello" {
		t.Fatalf("message text = %q, want hello", streamMessageText(event.Message))
	}
}

func TestStreamProcessorSystemStatusStringMessageEmitsWarning(t *testing.T) {
	var got []llm.StreamEvent
	p := newStreamProcessor(func(ev llm.StreamEvent) error {
		got = append(got, ev)
		return nil
	})
	if err := p.handleEnvelope(cliStreamEnvelope{
		Type:        "system",
		Subtype:     "status",
		Status:      "rate_limited",
		MessageText: "Retrying after rate limit",
	}); err != nil {
		t.Fatalf("handleEnvelope: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Type != llm.StreamEventRuntimeWarning ||
		got[0].RuntimeStatus != "rate_limited" ||
		got[0].RuntimeWarning != "Retrying after rate limit" {
		t.Fatalf("got %+v, want runtime_warning rate_limited/Retrying after rate limit", got[0])
	}
}

func TestStreamProcessorSystemStatusEmitsWarning(t *testing.T) {
	var got []llm.StreamEvent
	p := newStreamProcessor(func(ev llm.StreamEvent) error {
		got = append(got, ev)
		return nil
	})
	if err := p.handleEnvelope(cliStreamEnvelope{
		Type:    "system",
		Subtype: "status",
		Status:  "compacting",
	}); err != nil {
		t.Fatalf("handleEnvelope: %v", err)
	}
	if len(got) != 1 || got[0].Type != llm.StreamEventRuntimeWarning || got[0].RuntimeStatus != "compacting" {
		t.Fatalf("got %+v, want runtime_warning compacting", got)
	}
}

func TestStreamProcessorUserToolResultCorrelatesToolName(t *testing.T) {
	p := newStreamProcessor(nil)
	p.toolsByID["toolu_1"] = &toolBlockState{id: "toolu_1", name: "Read"}

	var got []llm.StreamEvent
	p.onEvent = func(ev llm.StreamEvent) error {
		got = append(got, ev)
		return nil
	}

	if err := p.handleEnvelope(cliStreamEnvelope{
		Type: "user",
		Message: cliStreamMessage{
			Content: []cliStreamContent{{
				Type:      "tool_result",
				ToolUseID: "toolu_1",
				Content:   json.RawMessage(`"done"`),
			}},
		},
	}); err != nil {
		t.Fatalf("handleEnvelope: %v", err)
	}
	if len(got) != 2 || got[0].Type != llm.StreamEventToolOutput || got[1].Type != llm.StreamEventToolCompleted {
		t.Fatalf("got %+v, want tool_output then tool_completed", got)
	}
	if got[0].ToolCallName != "Read" || got[0].ToolOutput != "done" {
		t.Fatalf("tool_output = %+v, want Read/done", got[0])
	}
}

func TestStreamProcessorAssistantShapedUserToolResult(t *testing.T) {
	p := newStreamProcessor(nil)
	p.toolsByID["toolu_1"] = &toolBlockState{id: "toolu_1", name: "Bash"}

	var got []llm.StreamEvent
	p.onEvent = func(ev llm.StreamEvent) error {
		got = append(got, ev)
		return nil
	}

	if err := p.handleEnvelope(cliStreamEnvelope{
		Type: "assistant",
		Message: cliStreamMessage{
			Role: "user",
			Content: []cliStreamContent{{
				Type:      "tool_result",
				ToolUseID: "toolu_1",
				Content:   json.RawMessage(`"ok"`),
				IsError:   true,
			}},
		},
	}); err != nil {
		t.Fatalf("handleEnvelope: %v", err)
	}
	if len(got) != 2 || got[0].Type != llm.StreamEventToolOutput || got[1].Type != llm.StreamEventToolCompleted {
		t.Fatalf("got %+v, want tool_output then tool_completed", got)
	}
	if got[0].ToolCallName != "Bash" || got[0].ToolOutput != "ok" || !got[0].ToolIsError {
		t.Fatalf("tool_output = %+v, want Bash/ok error", got[0])
	}
}

func TestStreamProcessorAssistantToolUseFallbackDoesNotDuplicateLifecycle(t *testing.T) {
	p := newStreamProcessor(nil)
	p.emittedToolInputCompleted["toolu_1"] = true
	p.toolsByID["toolu_1"] = &toolBlockState{id: "toolu_1", name: "Read", inputCompleted: true}

	events := 0
	p.onEvent = func(ev llm.StreamEvent) error {
		events++
		return nil
	}

	if err := p.handleEnvelope(cliStreamEnvelope{
		Type: "assistant",
		Message: cliStreamMessage{
			Content: []cliStreamContent{{
				Type:  "tool_use",
				ID:    "toolu_1",
				Name:  "Read",
				Input: json.RawMessage(`{"file_path":"foo.go"}`),
			}},
		},
	}); err != nil {
		t.Fatalf("handleEnvelope: %v", err)
	}
	if events != 0 {
		t.Fatalf("expected no duplicate lifecycle events, got %d", events)
	}
}

func TestStreamProcessorAssistantToolUseFallbackPropagatesEmitError(t *testing.T) {
	emitErr := errors.New("callback failed")
	p := newStreamProcessor(func(ev llm.StreamEvent) error {
		return emitErr
	})

	err := p.handleEnvelope(cliStreamEnvelope{
		Type: "assistant",
		Message: cliStreamMessage{
			Content: []cliStreamContent{{
				Type:  "tool_use",
				ID:    "toolu_1",
				Name:  "Read",
				Input: json.RawMessage(`{"file_path":"foo.go"}`),
			}},
		},
	})
	if !errors.Is(err, emitErr) {
		t.Fatalf("error = %v, want %v", err, emitErr)
	}
}

func TestStreamProcessorUserToolResultPropagatesEmitError(t *testing.T) {
	emitErr := errors.New("callback failed")
	p := newStreamProcessor(func(ev llm.StreamEvent) error {
		return emitErr
	})
	p.toolsByID["toolu_1"] = &toolBlockState{id: "toolu_1", name: "Read"}

	err := p.handleEnvelope(cliStreamEnvelope{
		Type: "user",
		Message: cliStreamMessage{
			Content: []cliStreamContent{{
				Type:      "tool_result",
				ToolUseID: "toolu_1",
				Content:   json.RawMessage(`"done"`),
			}},
		},
	})
	if !errors.Is(err, emitErr) {
		t.Fatalf("error = %v, want %v", err, emitErr)
	}
}

func TestStreamProcessorUserToolResultRetriesOutputAfterEmitError(t *testing.T) {
	emitErr := errors.New("callback failed")
	failOutput := true
	var got []llm.StreamEventType
	p := newStreamProcessor(func(ev llm.StreamEvent) error {
		got = append(got, ev.Type)
		if ev.Type == llm.StreamEventToolOutput && failOutput {
			failOutput = false
			return emitErr
		}
		return nil
	})
	p.toolsByID["toolu_1"] = &toolBlockState{id: "toolu_1", name: "Read"}
	event := cliStreamEnvelope{
		Type: "user",
		Message: cliStreamMessage{Content: []cliStreamContent{{
			Type: "tool_result", ToolUseID: "toolu_1", Content: json.RawMessage(`"done"`),
		}}},
	}

	if err := p.handleEnvelope(event); !errors.Is(err, emitErr) {
		t.Fatalf("first error = %v, want %v", err, emitErr)
	}
	if err := p.handleEnvelope(event); err != nil {
		t.Fatalf("retry handleEnvelope: %v", err)
	}
	want := []llm.StreamEventType{
		llm.StreamEventToolOutput,
		llm.StreamEventToolOutput,
		llm.StreamEventToolCompleted,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestStreamProcessorUserToolResultRetriesCompletionAfterEmitError(t *testing.T) {
	emitErr := errors.New("callback failed")
	failCompleted := true
	var got []llm.StreamEventType
	p := newStreamProcessor(func(ev llm.StreamEvent) error {
		got = append(got, ev.Type)
		if ev.Type == llm.StreamEventToolCompleted && failCompleted {
			failCompleted = false
			return emitErr
		}
		return nil
	})
	p.toolsByID["toolu_1"] = &toolBlockState{id: "toolu_1", name: "Read"}
	event := cliStreamEnvelope{
		Type: "user",
		Message: cliStreamMessage{Content: []cliStreamContent{{
			Type: "tool_result", ToolUseID: "toolu_1", Content: json.RawMessage(`"done"`),
		}}},
	}

	if err := p.handleEnvelope(event); !errors.Is(err, emitErr) {
		t.Fatalf("first error = %v, want %v", err, emitErr)
	}
	if err := p.handleEnvelope(event); err != nil {
		t.Fatalf("retry handleEnvelope: %v", err)
	}
	want := []llm.StreamEventType{
		llm.StreamEventToolOutput,
		llm.StreamEventToolCompleted,
		llm.StreamEventToolCompleted,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestStreamProcessorAssistantToolResultPropagatesEmitError(t *testing.T) {
	emitErr := errors.New("callback failed")
	p := newStreamProcessor(func(ev llm.StreamEvent) error {
		return emitErr
	})
	p.toolsByID["toolu_1"] = &toolBlockState{id: "toolu_1", name: "Bash"}

	err := p.handleEnvelope(cliStreamEnvelope{
		Type: "assistant",
		Message: cliStreamMessage{
			Content: []cliStreamContent{{
				Type:      "tool_result",
				ToolUseID: "toolu_1",
				Content:   json.RawMessage(`"ok"`),
			}},
		},
	})
	if !errors.Is(err, emitErr) {
		t.Fatalf("error = %v, want %v", err, emitErr)
	}
}
