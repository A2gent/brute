package claudecli

import (
	"encoding/json"
	"errors"
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

func TestStreamProcessorUnsupportedToolResultShapeEmitsOutput(t *testing.T) {
	p := newStreamProcessor(nil)
	p.toolsByID["toolu_1"] = &toolBlockState{id: "toolu_1", name: "Read"}

	var out llm.StreamEvent
	p.onEvent = func(ev llm.StreamEvent) error {
		out = ev
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
	if out.Type != llm.StreamEventToolOutput || out.ToolCallName != "Read" {
		t.Fatalf("got %+v, want tool_output Read", out)
	}
	if out.ToolOutput != `{"unexpected":"shape"}` || !out.ToolIsError {
		t.Fatalf("got output=%q isError=%v, want compact JSON with provider is_error", out.ToolOutput, out.ToolIsError)
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

	var out llm.StreamEvent
	p.onEvent = func(ev llm.StreamEvent) error {
		out = ev
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
	if out.Type != llm.StreamEventToolOutput || out.ToolCallName != "Read" || out.ToolOutput != "done" {
		t.Fatalf("got %+v, want tool_output Read/done", out)
	}
}

func TestStreamProcessorAssistantShapedUserToolResult(t *testing.T) {
	p := newStreamProcessor(nil)
	p.toolsByID["toolu_1"] = &toolBlockState{id: "toolu_1", name: "Bash"}

	var out llm.StreamEvent
	p.onEvent = func(ev llm.StreamEvent) error {
		out = ev
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
	if out.Type != llm.StreamEventToolOutput || out.ToolCallName != "Bash" || out.ToolOutput != "ok" || !out.ToolIsError {
		t.Fatalf("got %+v, want tool_output Bash/ok error", out)
	}
}

func TestStreamProcessorAssistantToolUseFallbackDoesNotDuplicateLifecycle(t *testing.T) {
	p := newStreamProcessor(nil)
	p.emittedToolCompleted["toolu_1"] = true
	p.toolsByID["toolu_1"] = &toolBlockState{id: "toolu_1", name: "Read", completed: true}

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
