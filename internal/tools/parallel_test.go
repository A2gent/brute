package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

type sleepTool struct{}

func (t *sleepTool) Name() string { return "test_sleep" }
func (t *sleepTool) Description() string {
	return "sleep then emit text"
}
func (t *sleepTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *sleepTool) Execute(_ context.Context, params json.RawMessage) (*Result, error) {
	var p struct {
		Text string `json:"text"`
		Ms   int    `json:"ms"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if p.Ms > 0 {
		time.Sleep(time.Duration(p.Ms) * time.Millisecond)
	}
	return &Result{Success: true, Output: p.Text}, nil
}

type outputOnlyFailTool struct{}

func (t *outputOnlyFailTool) Name() string        { return "test_output_fail" }
func (t *outputOnlyFailTool) Description() string { return "fails with structured output only" }
func (t *outputOnlyFailTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *outputOnlyFailTool) Execute(_ context.Context, _ json.RawMessage) (*Result, error) {
	return &Result{Success: false, Output: `[{"step":1,"error":"tool not found: missing"}]`}, nil
}

func TestParallelTool_Execute(t *testing.T) {
	manager := NewManager(t.TempDir())
	manager.Register(&emitTool{})
	manager.Register(&failTool{})
	manager.Register(&sleepTool{})
	manager.Register(&outputOnlyFailTool{})

	parallelRaw, ok := manager.Get("parallel")
	if !ok {
		t.Fatal("parallel tool not registered")
	}
	parallel, ok := parallelRaw.(*ParallelTool)
	if !ok {
		t.Fatalf("unexpected parallel tool type: %T", parallelRaw)
	}

	t.Run("runs independent steps concurrently and preserves order", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "test_sleep", "args": map[string]interface{}{"text": "first", "ms": 140}},
				{"tool": "test_sleep", "args": map[string]interface{}{"text": "second", "ms": 140}},
			},
		}
		raw, _ := json.Marshal(params)

		start := time.Now()
		result, err := parallel.Execute(context.Background(), raw)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if elapsed >= 240*time.Millisecond {
			t.Fatalf("expected concurrent execution, took %v", elapsed)
		}

		var outputs []parallelStepOutput
		if err := json.Unmarshal([]byte(result.Output), &outputs); err != nil {
			t.Fatalf("failed to decode output: %v\n%s", err, result.Output)
		}
		if len(outputs) != 2 {
			t.Fatalf("expected two outputs, got %d", len(outputs))
		}
		if outputs[0].Output != "first" || outputs[1].Output != "second" {
			t.Fatalf("unexpected output order: %#v", outputs)
		}
	})

	t.Run("returns all step results when one fails", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "test_emit", "args": map[string]interface{}{"text": "ok"}},
				{"tool": "test_fail"},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := parallel.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
		if !strings.Contains(result.Output, `"output": "ok"`) {
			t.Fatalf("expected successful step output, got: %s", result.Output)
		}
		if !strings.Contains(result.Output, `"error": "boom"`) {
			t.Fatalf("expected failed step error, got: %s", result.Output)
		}
	})

	t.Run("accepts inline step arguments", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "test_emit", "text": "inline"},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := parallel.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if !strings.Contains(result.Output, `"output": "inline"`) {
			t.Fatalf("expected inline arg output, got: %s", result.Output)
		}
	})

	t.Run("accepts provider namespaced tool names", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "functions.test_emit", "text": "namespaced"},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := parallel.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s output: %s", result.Error, result.Output)
		}
		if !strings.Contains(result.Output, `"tool": "test_emit"`) || !strings.Contains(result.Output, `"output": "namespaced"`) {
			t.Fatalf("expected namespaced tool to be normalized, got: %s", result.Output)
		}
	})

	t.Run("args takes precedence over inline arguments", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "test_emit", "text": "inline", "args": map[string]interface{}{"text": "nested"}},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := parallel.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if !strings.Contains(result.Output, `"output": "nested"`) {
			t.Fatalf("expected nested args to win, got: %s", result.Output)
		}
	})

	t.Run("invalid step args", func(t *testing.T) {
		params := `{"steps":[{"tool":"test_emit","args":["not","object"]}]}`
		result, err := parallel.Execute(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
		if !strings.Contains(result.Error, "args must be an object") {
			t.Fatalf("unexpected error: %s", result.Error)
		}
	})

	t.Run("disallow recursive parallel", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "parallel"},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := parallel.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
		if !strings.Contains(result.Error, "recursive parallel call") {
			t.Fatalf("unexpected error: %s", result.Error)
		}
	})

	t.Run("disallow namespaced recursive parallel", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "functions.parallel"},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := parallel.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
		if !strings.Contains(result.Error, "recursive parallel call") {
			t.Fatalf("unexpected error: %s", result.Error)
		}
	})

	t.Run("disallow sub-agent delegation", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "delegate_to_subagent"},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := parallel.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
		if !strings.Contains(result.Error, "sub-agent delegation must be called as a top-level tool call") {
			t.Fatalf("unexpected error: %s", result.Error)
		}
	})

	t.Run("disallow browser automation", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "browser_chrome", "action": "click", "selector": "#menu"},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := parallel.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
		if !strings.Contains(result.Error, "browser automation is stateful") {
			t.Fatalf("unexpected error: %s", result.Error)
		}
	})

	t.Run("returns when context is cancelled while a step is still running", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "test_sleep", "args": map[string]interface{}{"text": "late", "ms": 200}},
			},
		}
		raw, _ := json.Marshal(params)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		result, err := parallel.Execute(ctx, raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if time.Since(start) > 100*time.Millisecond {
			t.Fatalf("expected quick cancellation, took %v", time.Since(start))
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
		if !strings.Contains(result.Output, "context canceled") {
			t.Fatalf("expected cancellation in output, got: %s", result.Output)
		}
	})
}

func TestManagerExecuteParallel_PreservesOutputOnlyFailure(t *testing.T) {
	manager := NewManager(t.TempDir())
	manager.Register(&outputOnlyFailTool{})

	results := manager.ExecuteParallel(context.Background(), []llm.ToolCall{
		{
			ID:    "tc-output-fail",
			Name:  "functions.test_output_fail",
			Input: `{}`,
		},
	})

	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatalf("expected error result: %#v", results[0])
	}
	if results[0].Name != "test_output_fail" {
		t.Fatalf("expected normalized result name, got %q", results[0].Name)
	}
	if !strings.Contains(results[0].Content, "tool not found: missing") {
		t.Fatalf("expected structured failure output to be preserved, got: %s", results[0].Content)
	}
}
