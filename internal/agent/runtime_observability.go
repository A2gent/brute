package agent

import (
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm"
)

const (
	runtimeReasoningMaxBytes  = 32 * 1024
	runtimeToolOutputMaxBytes = 64 * 1024
	runtimeWarningsMaxCount   = 100
)

type runtimeObservabilityAccumulator struct {
	turnID             string
	persistReasoning   bool
	reasoning          string
	reasoningTruncated bool
	cost               map[string]interface{}
	warnings           []map[string]interface{}
	warningKeys        map[string]struct{}
	tools              map[string]map[string]interface{}
	nativeRuntime      bool
}

func newRuntimeObservabilityAccumulator(turnID string, persistReasoning bool) *runtimeObservabilityAccumulator {
	return &runtimeObservabilityAccumulator{
		turnID:           turnID,
		persistReasoning: persistReasoning,
		warningKeys:      make(map[string]struct{}),
		tools:            make(map[string]map[string]interface{}),
	}
}

func (a *runtimeObservabilityAccumulator) observe(ev llm.StreamEvent) {
	switch ev.Type {
	case llm.StreamEventReasoningDelta:
		a.observeReasoning(ev.ReasoningDelta)
	case llm.StreamEventToolStarted, llm.StreamEventToolUpdated,
		llm.StreamEventToolInputCompleted, llm.StreamEventToolCompleted:
		a.observeToolLifecycle(ev)
	case llm.StreamEventToolOutput:
		a.observeToolOutput(ev)
	case llm.StreamEventCost:
		a.cost = map[string]interface{}{
			"total_cost_usd":  ev.TotalCostUSD,
			"duration_ms":     ev.DurationMS,
			"duration_api_ms": ev.DurationAPIMS,
			"num_turns":       ev.NumTurns,
		}
	case llm.StreamEventRuntimeWarning:
		a.observeWarning(ev.RuntimeStatus, ev.RuntimeWarning)
	}
}

func (a *runtimeObservabilityAccumulator) observeReasoning(delta string) {
	if !a.persistReasoning || delta == "" {
		return
	}
	combined := a.reasoning + delta
	truncated, wasTruncated := truncateUTF8Bytes(combined, runtimeReasoningMaxBytes)
	a.reasoning = truncated
	if wasTruncated {
		a.reasoningTruncated = true
	}
}

func (a *runtimeObservabilityAccumulator) observeWarning(status, message string) {
	if status == "" && message == "" {
		return
	}
	key := status + "\x00" + message
	if _, exists := a.warningKeys[key]; exists {
		return
	}
	if len(a.warnings) >= runtimeWarningsMaxCount {
		return
	}
	a.warningKeys[key] = struct{}{}
	a.warnings = append(a.warnings, map[string]interface{}{
		"status":  status,
		"message": message,
	})
}

func (a *runtimeObservabilityAccumulator) observeToolLifecycle(ev llm.StreamEvent) {
	toolID := ev.ToolCallID
	if toolID == "" {
		return
	}
	a.nativeRuntime = true
	entry := a.tools[toolID]
	if entry == nil {
		entry = make(map[string]interface{})
	}
	entry["native_runtime"] = true
	entry["lifecycle"] = string(ev.Type)
	if ev.ToolCallName != "" {
		entry["name"] = ev.ToolCallName
	}
	if ev.ToolCallIndex != 0 || entry["index"] == nil {
		entry["index"] = ev.ToolCallIndex
	}
	if ev.ToolInputDelta != "" {
		entry["input_json"] = ev.ToolInputDelta
	}
	a.tools[toolID] = entry
}

func (a *runtimeObservabilityAccumulator) observeToolOutput(ev llm.StreamEvent) {
	toolID := ev.ToolCallID
	if toolID == "" {
		return
	}
	a.nativeRuntime = true
	entry := a.tools[toolID]
	if entry == nil {
		entry = make(map[string]interface{})
	}
	entry["native_runtime"] = true
	if ev.ToolCallName != "" {
		entry["name"] = ev.ToolCallName
	}
	output, truncated := truncateUTF8Bytes(ev.ToolOutput, runtimeToolOutputMaxBytes)
	if output != "" {
		entry["output"] = output
	}
	entry["is_error"] = ev.ToolIsError
	if truncated {
		entry["output_truncated"] = true
	}
	a.tools[toolID] = entry
}

func (a *runtimeObservabilityAccumulator) hasDurablePayload() bool {
	if a == nil {
		return false
	}
	if a.nativeRuntime || a.cost != nil || len(a.warnings) > 0 || len(a.tools) > 0 {
		return true
	}
	return a.persistReasoning && (a.reasoning != "" || a.reasoningTruncated)
}

func mergeRuntimeMetadata(base map[string]interface{}, acc *runtimeObservabilityAccumulator) map[string]interface{} {
	if acc == nil {
		return base
	}
	runtime := acc.metadata()
	if base == nil {
		return runtime
	}
	merged := make(map[string]interface{}, len(base)+len(runtime))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range runtime {
		merged[key] = value
	}
	return merged
}

func (a *runtimeObservabilityAccumulator) metadata() map[string]interface{} {
	md := map[string]interface{}{
		"runtime_turn_id": a.turnID,
	}
	if a.nativeRuntime {
		md["runtime_native"] = true
	}
	if len(a.tools) > 0 {
		md["runtime_tools"] = a.tools
	}
	if a.cost != nil {
		md["runtime_cost"] = a.cost
	}
	if len(a.warnings) > 0 {
		md["runtime_warnings"] = a.warnings
	}
	if a.persistReasoning && a.reasoning != "" {
		md["runtime_reasoning"] = a.reasoning
	}
	if a.reasoningTruncated {
		md["runtime_reasoning_truncated"] = true
	}
	return md
}

func truncateUTF8Bytes(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value, false
	}
	truncated := value[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated, true
}
