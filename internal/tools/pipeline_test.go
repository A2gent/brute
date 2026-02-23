package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type emitTool struct{}

func (t *emitTool) Name() string { return "test_emit" }
func (t *emitTool) Description() string {
	return "emit text"
}
func (t *emitTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *emitTool) Execute(_ context.Context, params json.RawMessage) (*Result, error) {
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return &Result{Success: true, Output: p.Text}, nil
}

type joinTool struct{}

func (t *joinTool) Name() string { return "test_join" }
func (t *joinTool) Description() string {
	return "join prefix and input"
}
func (t *joinTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *joinTool) Execute(_ context.Context, params json.RawMessage) (*Result, error) {
	var p struct {
		Prefix string `json:"prefix"`
		Input  string `json:"input"`
		Right  string `json:"right"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	right := p.Input
	if p.Right != "" {
		right = p.Right
	}
	return &Result{Success: true, Output: p.Prefix + right}, nil
}

type failTool struct{}

func (t *failTool) Name() string { return "test_fail" }
func (t *failTool) Description() string {
	return "always fails"
}
func (t *failTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *failTool) Execute(_ context.Context, _ json.RawMessage) (*Result, error) {
	return &Result{Success: false, Error: "boom"}, nil
}

func TestPipelineTool_Execute(t *testing.T) {
	manager := NewManager(t.TempDir())
	manager.Register(&emitTool{})
	manager.Register(&joinTool{})
	manager.Register(&failTool{})

	pipelineRaw, ok := manager.Get("pipeline")
	if !ok {
		t.Fatal("pipeline tool not registered")
	}
	pipeline, ok := pipelineRaw.(*PipelineTool)
	if !ok {
		t.Fatalf("unexpected pipeline tool type: %T", pipelineRaw)
	}

	t.Run("sequential chain with previous output injection", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "test_emit", "args": map[string]interface{}{"text": "hello"}},
				{"tool": "test_join", "args": map[string]interface{}{"prefix": "value:"}, "input_from_prev": true},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := pipeline.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if result.Output != "value:hello" {
			t.Fatalf("unexpected output: %q", result.Output)
		}
	})

	t.Run("custom input key", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "test_emit", "args": map[string]interface{}{"text": "R"}},
				{"tool": "test_join", "args": map[string]interface{}{"prefix": "L"}, "input_key": "right"},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := pipeline.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if result.Output != "LR" {
			t.Fatalf("unexpected output: %q", result.Output)
		}
	})

	t.Run("stage failure stops pipeline", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "test_emit", "args": map[string]interface{}{"text": "ignored"}},
				{"tool": "test_fail"},
				{"tool": "test_join", "args": map[string]interface{}{"prefix": "never"}, "input_from_prev": true},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := pipeline.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
		if !strings.Contains(result.Error, "step 2 (test_fail) failed") {
			t.Fatalf("unexpected error: %s", result.Error)
		}
	})

	t.Run("invalid stage args", func(t *testing.T) {
		params := `{"steps":[{"tool":"test_emit","args":["not","object"]}]}`
		result, err := pipeline.Execute(context.Background(), json.RawMessage(params))
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

	t.Run("disallow recursive pipeline", func(t *testing.T) {
		params := map[string]interface{}{
			"steps": []map[string]interface{}{
				{"tool": "pipeline"},
			},
		}
		raw, _ := json.Marshal(params)
		result, err := pipeline.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success {
			t.Fatalf("expected failure, got output: %s", result.Output)
		}
		if !strings.Contains(result.Error, "recursive pipeline call") {
			t.Fatalf("unexpected error: %s", result.Error)
		}
	})
}
