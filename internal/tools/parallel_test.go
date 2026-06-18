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

type fakeDelegationTool struct {
	name string
}

func (t *fakeDelegationTool) Name() string { return t.name }
func (t *fakeDelegationTool) Description() string {
	return "fake delegation tool for tests"
}
func (t *fakeDelegationTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *fakeDelegationTool) Execute(_ context.Context, params json.RawMessage) (*Result, error) {
	var p struct {
		AgentID    string `json:"agent_id"`
		SubAgentID string `json:"sub_agent_id"`
		Task       string `json:"task"`
		Ms         int    `json:"ms"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if p.Ms > 0 {
		time.Sleep(time.Duration(p.Ms) * time.Millisecond)
	}
	target := strings.TrimSpace(p.AgentID)
	if target == "" {
		target = strings.TrimSpace(p.SubAgentID)
	}
	return &Result{
		Success: true,
		Output:  "delegated " + target + ": " + p.Task,
		Metadata: map[string]interface{}{
			"child_session_id": "child-" + target,
		},
	}, nil
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

type metadataFailTool struct{}

func (t *metadataFailTool) Name() string        { return "test_metadata_fail" }
func (t *metadataFailTool) Description() string { return "fails with metadata" }
func (t *metadataFailTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *metadataFailTool) Execute(_ context.Context, _ json.RawMessage) (*Result, error) {
	return &Result{
		Success: false,
		Error:   "child failed",
		Metadata: map[string]interface{}{
			"child_session_id": "child-1",
		},
	}, nil
}

type progressTool struct{}

func (t *progressTool) Name() string        { return "test_progress" }
func (t *progressTool) Description() string { return "emits progress before returning" }
func (t *progressTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *progressTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, error) {
	ReportProgress(ctx, ProgressEvent{
		Status:  "child_session_created",
		Content: "child session ready",
		Metadata: map[string]interface{}{
			"child_session_id": "child-progress",
		},
	})
	return &Result{Success: true, Output: "done"}, nil
}

func TestParallelTool_Execute(t *testing.T) {
	manager := NewManager(t.TempDir())
	manager.Register(&emitTool{})
	manager.Register(&failTool{})
	manager.Register(&sleepTool{})
	manager.Register(&outputOnlyFailTool{})
	manager.Register(&progressTool{})
	manager.Register(&fakeDelegationTool{name: "delegate_to_agent"})
	manager.Register(&fakeDelegationTool{name: "delegate_to_subagent"})

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

	t.Run("allows parallel agent delegation", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "delegate_to_agent", "args": map[string]interface{}{"agent_id": "researcher", "task": "inspect api", "ms": 140}},
				{"tool": "delegate_to_subagent", "args": map[string]interface{}{"sub_agent_id": "tester", "task": "write test plan", "ms": 140}},
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
			t.Fatalf("expected success, got error: %s output: %s", result.Error, result.Output)
		}
		if elapsed >= 240*time.Millisecond {
			t.Fatalf("expected delegation steps to run concurrently, took %v", elapsed)
		}

		var outputs []parallelStepOutput
		if err := json.Unmarshal([]byte(result.Output), &outputs); err != nil {
			t.Fatalf("failed to decode output: %v\n%s", err, result.Output)
		}
		if len(outputs) != 2 {
			t.Fatalf("expected two outputs, got %d", len(outputs))
		}
		if outputs[0].Tool != "delegate_to_agent" || !strings.Contains(outputs[0].Output, "delegated researcher") {
			t.Fatalf("unexpected delegate_to_agent output: %#v", outputs[0])
		}
		if outputs[1].Tool != "delegate_to_subagent" || !strings.Contains(outputs[1].Output, "delegated tester") {
			t.Fatalf("unexpected delegate_to_subagent output: %#v", outputs[1])
		}
		if outputs[0].Metadata["child_session_id"] != "child-researcher" || outputs[1].Metadata["child_session_id"] != "child-tester" {
			t.Fatalf("expected child session metadata to be preserved, got %#v", outputs)
		}
	})

	t.Run("annotates nested progress with parallel step metadata", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "test_progress", "args": map[string]interface{}{}},
			},
		}
		raw, _ := json.Marshal(params)
		var progressEvents []ProgressEvent
		ctx := context.WithValue(context.Background(), "tool_call_id", "call-parallel")
		ctx = WithProgressCallback(ctx, func(event ProgressEvent) {
			progressEvents = append(progressEvents, event)
		})

		result, err := parallel.Execute(ctx, raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if len(progressEvents) != 1 {
			t.Fatalf("expected one progress event, got %#v", progressEvents)
		}
		event := progressEvents[0]
		if event.ToolCallID != "call-parallel" {
			t.Fatalf("progress tool_call_id = %q, want parent parallel call id", event.ToolCallID)
		}
		if event.ToolName != "test_progress" {
			t.Fatalf("progress tool_name = %q, want test_progress", event.ToolName)
		}
		if event.Metadata["parallel_step"] != 1 {
			t.Fatalf("parallel_step = %#v, want 1", event.Metadata["parallel_step"])
		}
		if event.Metadata["parallel_tool"] != "test_progress" {
			t.Fatalf("parallel_tool = %#v, want test_progress", event.Metadata["parallel_tool"])
		}
		if event.Metadata["child_session_id"] != "child-progress" {
			t.Fatalf("child_session_id not preserved: %#v", event.Metadata)
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

	t.Run("disallow session suggestions inside parallel", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "suggest_session", "title": "Follow up", "prompt": "Inspect separately."},
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
		if !strings.Contains(result.Error, "top-level tool calls") {
			t.Fatalf("unexpected error: %s", result.Error)
		}
	})

	t.Run("disallow git commit suggestions inside parallel", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "suggest_git_commit", "message": "Update app", "files": []string{"src/app.ts"}},
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
		if !strings.Contains(result.Error, "top-level tool calls") {
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

func TestManagerExecuteParallel_PreservesFailureMetadata(t *testing.T) {
	manager := NewManager(t.TempDir())
	manager.Register(&metadataFailTool{})

	results := manager.ExecuteParallel(context.Background(), []llm.ToolCall{
		{
			ID:    "tc-metadata-fail",
			Name:  "test_metadata_fail",
			Input: `{}`,
		},
	})

	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatalf("expected error result: %#v", results[0])
	}
	if results[0].Metadata["child_session_id"] != "child-1" {
		t.Fatalf("expected failure metadata to be preserved, got %#v", results[0].Metadata)
	}
}

func TestParallelTimeoutForTool(t *testing.T) {
	t.Setenv(parallelDelegationTimeoutEnv, "")

	if got := parallelTimeoutForTool("grep"); got != parallelStepTimeout {
		t.Fatalf("expected grep to use default timeout %s, got %s", parallelStepTimeout, got)
	}
	for _, name := range []string{"delegate_to_agent", "delegate_to_subagent", "functions.delegate_to_external_agent"} {
		if got := parallelTimeoutForTool(name); got != parallelDelegationStepTimeout {
			t.Fatalf("expected %s to use delegation timeout %s, got %s", name, parallelDelegationStepTimeout, got)
		}
	}

	t.Setenv(parallelDelegationTimeoutEnv, "24h")
	if got := parallelTimeoutForTool("delegate_to_agent"); got != 24*time.Hour {
		t.Fatalf("expected env override to control delegation timeout, got %s", got)
	}

	t.Setenv(parallelDelegationTimeoutEnv, "not-a-duration")
	if got := parallelTimeoutForTool("delegate_to_agent"); got != parallelDelegationStepTimeout {
		t.Fatalf("expected invalid env override to fall back to default %s, got %s", parallelDelegationStepTimeout, got)
	}
}
