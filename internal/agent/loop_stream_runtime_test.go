package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type mockStreamingLLM struct {
	MockLLM
	streamEvents []llm.StreamEvent
	response     *llm.ChatResponse
}

func (m *mockStreamingLLM) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	m.CapturedRequest = request
	m.CapturedRequests = append(m.CapturedRequests, request)
	if m.Err != nil {
		return nil, m.Err
	}
	for _, ev := range m.streamEvents {
		if onEvent != nil {
			if err := onEvent(ev); err != nil {
				return nil, err
			}
		}
	}
	if m.response != nil {
		return m.response, nil
	}
	return m.Response, nil
}

func TestCallLLMForwardsStructuredRuntimeEventsUnchanged(t *testing.T) {
	streamEvents := []llm.StreamEvent{
		{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "thinking"},
		{Type: llm.StreamEventToolStarted, ToolCallID: "toolu_1", ToolCallName: "Read", ToolCallIndex: 2},
		{Type: llm.StreamEventToolUpdated, ToolCallID: "toolu_1", ToolInputDelta: `{"file_path":"foo.go"}`},
		{Type: llm.StreamEventToolInputCompleted, ToolCallID: "toolu_1", ToolCallName: "Read", ToolInputDelta: `{"file_path":"foo.go"}`},
		{Type: llm.StreamEventToolOutput, ToolCallID: "toolu_1", ToolCallName: "Read", ToolOutput: "package main\n"},
		{Type: llm.StreamEventToolCompleted, ToolCallID: "toolu_1", ToolCallName: "Read", ToolInputDelta: `{"file_path":"foo.go"}`},
		{Type: llm.StreamEventCost, TotalCostUSD: 0.01, DurationMS: 100, DurationAPIMS: 90, NumTurns: 1},
		{Type: llm.StreamEventRuntimeWarning, RuntimeStatus: "compacting", RuntimeWarning: "compacting"},
		{Type: llm.StreamEventUsage, Usage: llm.TokenUsage{InputTokens: 10, OutputTokens: 5}},
		{Type: llm.StreamEventContentDelta, ContentDelta: "done"},
	}

	mock := &mockStreamingLLM{
		streamEvents: streamEvents,
		response:     &llm.ChatResponse{Content: "done"},
	}
	ag := New(Config{}, mock, nil, nil)

	var got []Event
	_, _, err := ag.callLLM(context.Background(), &llm.ChatRequest{}, 3, func(ev Event) {
		got = append(got, ev)
	})
	if err != nil {
		t.Fatalf("callLLM returned error: %v", err)
	}

	var turnID string
	wantRuntime := []llm.StreamEventType{
		llm.StreamEventReasoningDelta,
		llm.StreamEventToolStarted,
		llm.StreamEventToolUpdated,
		llm.StreamEventToolInputCompleted,
		llm.StreamEventToolOutput,
		llm.StreamEventToolCompleted,
		llm.StreamEventCost,
		llm.StreamEventRuntimeWarning,
	}
	runtimeIdx := 0
	for _, ev := range got {
		switch ev.Type {
		case EventAssistantDelta:
			if ev.Step != 3 || ev.Delta != "done" {
				t.Fatalf("assistant delta = step:%d delta:%q, want step:3 delta:done", ev.Step, ev.Delta)
			}
			if ev.TurnID == "" {
				t.Fatal("assistant delta missing turn_id")
			}
			if turnID == "" {
				turnID = ev.TurnID
			} else if ev.TurnID != turnID {
				t.Fatalf("assistant delta turn_id = %q, want %q", ev.TurnID, turnID)
			}
		case EventLLMRuntime:
			if runtimeIdx >= len(wantRuntime) {
				t.Fatalf("unexpected extra runtime event: %+v", ev.Runtime)
			}
			if ev.Step != 3 {
				t.Fatalf("runtime event step = %d, want 3", ev.Step)
			}
			if ev.TurnID == "" {
				t.Fatal("runtime event missing turn_id")
			}
			if turnID == "" {
				turnID = ev.TurnID
			} else if ev.TurnID != turnID {
				t.Fatalf("runtime event turn_id = %q, want %q", ev.TurnID, turnID)
			}
			if ev.Runtime == nil {
				t.Fatal("runtime event missing payload")
			}
			if ev.Runtime.Type != wantRuntime[runtimeIdx] {
				t.Fatalf("runtime[%d] type = %q, want %q", runtimeIdx, ev.Runtime.Type, wantRuntime[runtimeIdx])
			}
			if err := assertRuntimePayloadMatches(streamEvents[runtimeIdx], *ev.Runtime); err != nil {
				t.Fatalf("runtime[%d] payload mismatch: %v", runtimeIdx, err)
			}
			runtimeIdx++
		default:
			t.Fatalf("unexpected event type %q", ev.Type)
		}
	}
	if runtimeIdx != len(wantRuntime) {
		t.Fatalf("got %d runtime events, want %d", runtimeIdx, len(wantRuntime))
	}
	if turnID == "" {
		t.Fatal("expected non-empty shared turn_id")
	}
}

func TestCallLLMAccumulatorObservesWithoutOnEvent(t *testing.T) {
	mock := &mockStreamingLLM{
		streamEvents: []llm.StreamEvent{
			{Type: llm.StreamEventCost, TotalCostUSD: 0.01, DurationMS: 100, DurationAPIMS: 90, NumTurns: 1},
			{Type: llm.StreamEventContentDelta, ContentDelta: "done"},
		},
		response: &llm.ChatResponse{Content: "done"},
	}
	ag := New(Config{}, mock, nil, nil)

	_, acc, err := ag.callLLM(context.Background(), &llm.ChatRequest{}, 1, nil)
	if err != nil {
		t.Fatalf("callLLM returned error: %v", err)
	}
	md := acc.metadata()
	if md["runtime_turn_id"] == "" {
		t.Fatalf("runtime_turn_id missing: %#v", md)
	}
	cost, ok := md["runtime_cost"].(map[string]interface{})
	if !ok || cost["total_cost_usd"] != 0.01 {
		t.Fatalf("runtime_cost = %#v", md["runtime_cost"])
	}
}

func TestRunWithEventsPersistsRuntimeMetadataOnAssistantMessage(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("hello")

	mock := &mockStreamingLLM{
		streamEvents: []llm.StreamEvent{
			{Type: llm.StreamEventCost, TotalCostUSD: 0.02, DurationMS: 200, DurationAPIMS: 180, NumTurns: 1},
			{Type: llm.StreamEventContentDelta, ContentDelta: "Hi there"},
		},
		response: &llm.ChatResponse{Content: "Hi there"},
	}
	ag := New(Config{MaxSteps: 3}, mock, tools.NewManager(t.TempDir()), sm)

	_, _, err = ag.RunWithEvents(context.Background(), sess, "hello", nil)
	if err != nil {
		t.Fatalf("RunWithEvents returned error: %v", err)
	}
	if len(sess.Messages) < 2 {
		t.Fatalf("expected assistant message, got %#v", sess.Messages)
	}
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != "assistant" || last.Content != "Hi there" {
		t.Fatalf("assistant message = %#v", last)
	}
	if last.Metadata["runtime_turn_id"] == "" {
		t.Fatalf("missing runtime_turn_id metadata: %#v", last.Metadata)
	}
	if last.Metadata["llm_duration_ms"] == nil {
		t.Fatalf("missing timing metadata: %#v", last.Metadata)
	}
	cost, ok := last.Metadata["runtime_cost"].(map[string]interface{})
	if !ok || cost["total_cost_usd"] != 0.02 {
		t.Fatalf("runtime_cost = %#v", last.Metadata["runtime_cost"])
	}
}

func assertRuntimePayloadMatches(want, got llm.StreamEvent) error {
	if want.Type != got.Type {
		return errors.New("type mismatch")
	}
	switch want.Type {
	case llm.StreamEventReasoningDelta:
		if want.ReasoningDelta != got.ReasoningDelta {
			return errors.New("reasoning delta mismatch")
		}
	case llm.StreamEventToolStarted, llm.StreamEventToolUpdated, llm.StreamEventToolInputCompleted, llm.StreamEventToolCompleted:
		if want.ToolCallID != got.ToolCallID || want.ToolCallName != got.ToolCallName ||
			want.ToolCallIndex != got.ToolCallIndex || want.ToolInputDelta != got.ToolInputDelta {
			return errors.New("tool lifecycle mismatch")
		}
	case llm.StreamEventToolOutput:
		if want.ToolCallID != got.ToolCallID || want.ToolCallName != got.ToolCallName ||
			want.ToolOutput != got.ToolOutput || want.ToolIsError != got.ToolIsError {
			return errors.New("tool output mismatch")
		}
	case llm.StreamEventCost:
		if want.TotalCostUSD != got.TotalCostUSD || want.DurationMS != got.DurationMS ||
			want.DurationAPIMS != got.DurationAPIMS || want.NumTurns != got.NumTurns {
			return errors.New("cost mismatch")
		}
	case llm.StreamEventRuntimeWarning:
		if want.RuntimeStatus != got.RuntimeStatus || want.RuntimeWarning != got.RuntimeWarning {
			return errors.New("runtime warning mismatch")
		}
	}
	return nil
}

func TestRunWithEventsDoesNotExecuteNativeRuntimeTools(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sm := session.NewManager(store)
	sess, err := sm.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("read foo.go")

	mock := &mockStreamingLLM{
		streamEvents: []llm.StreamEvent{
			{Type: llm.StreamEventToolStarted, ToolCallID: "toolu_1", ToolCallName: "Read"},
			{Type: llm.StreamEventToolInputCompleted, ToolCallID: "toolu_1", ToolCallName: "Read", ToolInputDelta: `{"file_path":"foo.go"}`},
			{Type: llm.StreamEventToolOutput, ToolCallID: "toolu_1", ToolCallName: "Read", ToolOutput: "package main\n"},
			{Type: llm.StreamEventToolCompleted, ToolCallID: "toolu_1", ToolCallName: "Read", ToolInputDelta: `{"file_path":"foo.go"}`},
			{Type: llm.StreamEventContentDelta, ContentDelta: "Read foo.go successfully."},
		},
		response: &llm.ChatResponse{
			Content:   "Read foo.go successfully.",
			ToolCalls: nil,
		},
	}
	ag := New(Config{MaxSteps: 3}, mock, tools.NewManager(t.TempDir()), sm)

	var events []Event
	content, _, err := ag.RunWithEvents(context.Background(), sess, "read foo.go", func(ev Event) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("RunWithEvents returned error: %v", err)
	}
	if content != "Read foo.go successfully." {
		t.Fatalf("content = %q, want success message", content)
	}
	for _, ev := range events {
		if ev.Type == EventToolExecuting {
			t.Fatalf("native runtime tools must not trigger A2gent execution, got %#v", ev)
		}
	}
}
