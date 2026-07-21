package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm"
)

func TestRuntimeObservabilityMetadataAlwaysIncludesTurnID(t *testing.T) {
	acc := newRuntimeObservabilityAccumulator("turn-abc", false)
	md := acc.metadata()
	if md["runtime_turn_id"] != "turn-abc" {
		t.Fatalf("runtime_turn_id = %#v, want turn-abc", md["runtime_turn_id"])
	}
}

func TestRuntimeObservabilityCostReplaces(t *testing.T) {
	acc := newRuntimeObservabilityAccumulator("turn-1", false)
	acc.observe(llm.StreamEvent{
		Type:          llm.StreamEventCost,
		TotalCostUSD:  0.1,
		DurationMS:    100,
		DurationAPIMS: 90,
		NumTurns:      1,
	})
	acc.observe(llm.StreamEvent{
		Type:          llm.StreamEventCost,
		TotalCostUSD:  0.2,
		DurationMS:    200,
		DurationAPIMS: 180,
		NumTurns:      2,
	})

	md := acc.metadata()
	cost, ok := md["runtime_cost"].(map[string]interface{})
	if !ok {
		t.Fatalf("runtime_cost = %#v, want map", md["runtime_cost"])
	}
	if cost["total_cost_usd"] != 0.2 || cost["duration_ms"] != int64(200) ||
		cost["duration_api_ms"] != int64(180) || cost["num_turns"] != 2 {
		t.Fatalf("runtime_cost = %#v, want replaced second cost", cost)
	}
}

func TestRuntimeObservabilityWarningsDedupeAndCap(t *testing.T) {
	acc := newRuntimeObservabilityAccumulator("turn-1", false)
	acc.observe(llm.StreamEvent{
		Type:           llm.StreamEventRuntimeWarning,
		RuntimeStatus:  "rate_limited",
		RuntimeWarning: "Retrying",
	})
	acc.observe(llm.StreamEvent{
		Type:           llm.StreamEventRuntimeWarning,
		RuntimeStatus:  "rate_limited",
		RuntimeWarning: "Retrying",
	})
	for i := 0; i < 100; i++ {
		acc.observe(llm.StreamEvent{
			Type:           llm.StreamEventRuntimeWarning,
			RuntimeStatus:  "status",
			RuntimeWarning: strings.Repeat("w", i+1),
		})
	}

	md := acc.metadata()
	warnings, ok := md["runtime_warnings"].([]map[string]interface{})
	if !ok {
		t.Fatalf("runtime_warnings = %#v, want []map[string]interface{}", md["runtime_warnings"])
	}
	if len(warnings) != 100 {
		t.Fatalf("len(runtime_warnings) = %d, want 100", len(warnings))
	}
	if warnings[0]["status"] != "rate_limited" || warnings[0]["message"] != "Retrying" {
		t.Fatalf("first warning = %#v", warnings[0])
	}
}

func TestRuntimeObservabilityToolsUpsertLifecycle(t *testing.T) {
	acc := newRuntimeObservabilityAccumulator("turn-1", false)
	acc.observe(llm.StreamEvent{
		Type:          llm.StreamEventToolStarted,
		ToolCallID:    "tool-1",
		ToolCallName:  "Read",
		ToolCallIndex: 2,
	})
	acc.observe(llm.StreamEvent{
		Type:           llm.StreamEventToolUpdated,
		ToolCallID:     "tool-1",
		ToolInputDelta: `{"path":"/tmp/a.txt"`,
	})
	acc.observe(llm.StreamEvent{
		Type:           llm.StreamEventToolInputCompleted,
		ToolCallID:     "tool-1",
		ToolCallName:   "Read",
		ToolInputDelta: `{"path":"/tmp/a.txt"}`,
	})
	acc.observe(llm.StreamEvent{
		Type:         llm.StreamEventToolOutput,
		ToolCallID:   "tool-1",
		ToolCallName: "Read",
		ToolOutput:   "file contents",
		ToolIsError:  false,
	})
	acc.observe(llm.StreamEvent{
		Type:           llm.StreamEventToolCompleted,
		ToolCallID:     "tool-1",
		ToolCallName:   "Read",
		ToolInputDelta: `{"path":"/tmp/a.txt"}`,
	})

	md := acc.metadata()
	if md["runtime_native"] != true {
		t.Fatalf("runtime_native = %#v, want true", md["runtime_native"])
	}
	tools, ok := md["runtime_tools"].(map[string]map[string]interface{})
	if !ok {
		t.Fatalf("runtime_tools = %#v", md["runtime_tools"])
	}
	tool := tools["tool-1"]
	if tool["native_runtime"] != true || tool["lifecycle"] != "tool_completed" ||
		tool["name"] != "Read" || tool["index"] != 2 ||
		tool["input_json"] != `{"path":"/tmp/a.txt"}` ||
		tool["output"] != "file contents" || tool["is_error"] != false {
		t.Fatalf("tool-1 = %#v", tool)
	}
	if _, ok := tool["output_truncated"]; ok {
		t.Fatalf("unexpected output_truncated on short output: %#v", tool)
	}
}

func TestRuntimeObservabilityToolOutputCap(t *testing.T) {
	acc := newRuntimeObservabilityAccumulator("turn-1", false)
	output := strings.Repeat("o", runtimeToolOutputMaxBytes+10)
	acc.observe(llm.StreamEvent{
		Type:       llm.StreamEventToolOutput,
		ToolCallID: "tool-1",
		ToolOutput: output,
	})

	md := acc.metadata()
	tools := md["runtime_tools"].(map[string]map[string]interface{})
	tool := tools["tool-1"]
	gotOutput, ok := tool["output"].(string)
	if !ok {
		t.Fatalf("output = %#v", tool["output"])
	}
	if len(gotOutput) > runtimeToolOutputMaxBytes {
		t.Fatalf("output len = %d, want <= %d", len(gotOutput), runtimeToolOutputMaxBytes)
	}
	if !utf8.ValidString(gotOutput) {
		t.Fatal("output is not valid UTF-8")
	}
	if tool["output_truncated"] != true {
		t.Fatalf("output_truncated = %#v, want true", tool["output_truncated"])
	}
}

func TestRuntimeObservabilityReasoningOptInAndCap(t *testing.T) {
	without := newRuntimeObservabilityAccumulator("turn-1", false)
	without.observe(llm.StreamEvent{
		Type:           llm.StreamEventReasoningDelta,
		ReasoningDelta: "secret thought",
	})
	md := without.metadata()
	if _, ok := md["runtime_reasoning"]; ok {
		t.Fatalf("unexpected runtime_reasoning without opt-in: %#v", md)
	}

	with := newRuntimeObservabilityAccumulator("turn-1", true)
	with.observe(llm.StreamEvent{
		Type:           llm.StreamEventReasoningDelta,
		ReasoningDelta: "think",
	})
	md = with.metadata()
	if md["runtime_reasoning"] != "think" {
		t.Fatalf("runtime_reasoning = %#v, want think", md["runtime_reasoning"])
	}

	long := newRuntimeObservabilityAccumulator("turn-1", true)
	long.observe(llm.StreamEvent{
		Type:           llm.StreamEventReasoningDelta,
		ReasoningDelta: strings.Repeat("r", runtimeReasoningMaxBytes+10),
	})
	md = long.metadata()
	reasoning, ok := md["runtime_reasoning"].(string)
	if !ok {
		t.Fatalf("runtime_reasoning = %#v", md["runtime_reasoning"])
	}
	if len(reasoning) > runtimeReasoningMaxBytes {
		t.Fatalf("reasoning len = %d, want <= %d", len(reasoning), runtimeReasoningMaxBytes)
	}
	if !utf8.ValidString(reasoning) {
		t.Fatal("reasoning is not valid UTF-8")
	}
	if md["runtime_reasoning_truncated"] != true {
		t.Fatalf("runtime_reasoning_truncated = %#v, want true", md["runtime_reasoning_truncated"])
	}
}

func TestRuntimeObservabilityEmptyEventsNoBogusFields(t *testing.T) {
	acc := newRuntimeObservabilityAccumulator("turn-1", true)
	acc.observe(llm.StreamEvent{Type: llm.StreamEventReasoningDelta})
	acc.observe(llm.StreamEvent{Type: llm.StreamEventRuntimeWarning})
	acc.observe(llm.StreamEvent{Type: llm.StreamEventToolStarted})
	acc.observe(llm.StreamEvent{Type: llm.StreamEventToolOutput})
	acc.observe(llm.StreamEvent{Type: llm.StreamEventUsage})
	acc.observe(llm.StreamEvent{Type: llm.StreamEventContentDelta})

	md := acc.metadata()
	for _, key := range []string{
		"runtime_reasoning",
		"runtime_reasoning_truncated",
		"runtime_warnings",
		"runtime_tools",
		"runtime_native",
		"runtime_cost",
	} {
		if _, ok := md[key]; ok {
			t.Fatalf("unexpected %s in metadata: %#v", key, md)
		}
	}
	if md["runtime_turn_id"] != "turn-1" {
		t.Fatalf("runtime_turn_id = %#v, want turn-1", md["runtime_turn_id"])
	}
}

func TestRuntimeObservabilityHasDurablePayload(t *testing.T) {
	turnOnly := newRuntimeObservabilityAccumulator("turn-1", false)
	if turnOnly.hasDurablePayload() {
		t.Fatal("turn id alone should not be durable payload")
	}

	withCost := newRuntimeObservabilityAccumulator("turn-1", false)
	withCost.observe(llm.StreamEvent{Type: llm.StreamEventCost, TotalCostUSD: 0.1})
	if !withCost.hasDurablePayload() {
		t.Fatal("expected cost payload to be durable")
	}

	withReasoning := newRuntimeObservabilityAccumulator("turn-1", true)
	withReasoning.observe(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "think"})
	if !withReasoning.hasDurablePayload() {
		t.Fatal("expected reasoning payload to be durable when opt-in")
	}
}

func TestRuntimeObservabilityUTF8Truncation(t *testing.T) {
	acc := newRuntimeObservabilityAccumulator("turn-1", true)
	// 3-byte UTF-8 rune split across cap boundary.
	delta := strings.Repeat("a", runtimeReasoningMaxBytes-1) + "€extra"
	acc.observe(llm.StreamEvent{
		Type:           llm.StreamEventReasoningDelta,
		ReasoningDelta: delta,
	})

	md := acc.metadata()
	reasoning := md["runtime_reasoning"].(string)
	if !utf8.ValidString(reasoning) {
		t.Fatal("reasoning is not valid UTF-8")
	}
	if len(reasoning) > runtimeReasoningMaxBytes {
		t.Fatalf("reasoning len = %d, want <= %d", len(reasoning), runtimeReasoningMaxBytes)
	}
}
