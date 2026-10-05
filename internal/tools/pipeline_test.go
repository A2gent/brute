package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

// pipelineProbeTool lets gating tests stay independent of classify and external APIs.
type pipelineProbeTool struct {
	name string
	run  func(context.Context, json.RawMessage) (*Result, error)
}

func (t *pipelineProbeTool) Name() string        { return t.name }
func (t *pipelineProbeTool) Description() string { return "pipeline fake" }
func (t *pipelineProbeTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *pipelineProbeTool) Execute(ctx context.Context, raw json.RawMessage) (*Result, error) {
	return t.run(ctx, raw)
}

func TestPipelineTool_PerItem(t *testing.T) {
	for _, tc := range []struct {
		name, source, options, want, errorText string
	}{
		{"lines map", "a\n\nb\n", ``, `["read:a","read:b"]`, ""},
		{"JSON items", `["a","b"]`, ``, `["read:a","read:b"]`, ""},
		{"choice keep preserves input", "a\nb", `,"keep_if":{"field":"answer","op":"eq","value":"include"}`, `["read:a"]`, ""},
		{"choice drop", "a\nb", `,"drop_if":{"field":"answer","op":"eq","value":"include"}`, `["read:b"]`, ""},
		{"score threshold inclusive", "a\nb", `,"keep_if":{"field":"score","op":"gte","value":2}`, `["read:a"]`, ""},
		{"all dropped", "a\nb", `,"keep_if":{"field":"answer","op":"eq","value":"absent"}`, `[]`, ""},
		{"empty", "", ``, `[]`, ""},
		{"item limit", "a\nb", `,"max_items":1`, "", "too many"},
		{"hard limit", "a", `,"max_items":13`, "", "max_items"},
		{"bad operator", "a", `,"keep_if":{"field":"answer","op":"wat","value":"include"}`, "", "predicate"},
		{"missing field", "a", `,"keep_if":{"field":"missing","op":"eq","value":true}`, "", "missing"},
		{"bad numeric comparison", "a", `,"keep_if":{"field":"answer","op":"gte","value":2}`, "", "numeric"},
		{"both predicates", "a", `,"keep_if":{"field":"answer","op":"eq","value":"include"},"drop_if":{"field":"answer","op":"eq","value":"include"}`, "", "keep_if"},
		{"invalid JSON array", `[oops`, ``, "", "items"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(t.TempDir())
			manager.Register(&emitTool{})
			manager.Register(&pipelineProbeTool{name: "test_classify", run: func(_ context.Context, raw json.RawMessage) (*Result, error) {
				var p struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, err
				}
				if p.Path == "a" {
					return &Result{Success: true, Output: `{"answer":"include","score":2}`}, nil
				}
				return &Result{Success: true, Output: `{"answer":"skip","score":1}`}, nil
			}})
			manager.Register(&pipelineProbeTool{name: "test_read", run: func(_ context.Context, raw json.RawMessage) (*Result, error) {
				var p struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, err
				}
				return &Result{Success: true, Output: "read:" + p.Path}, nil
			}})
			source, _ := json.Marshal(tc.source)
			stage := `{"tool":"test_read","per_item":true,"input_key":"path"` + tc.options + `}`
			if strings.Contains(tc.options, "_if") {
				stage = `{"tool":"test_classify","per_item":true,"input_key":"path"` + tc.options + `},{"tool":"test_read","per_item":true,"input_key":"path"}`
			}
			raw := `{"steps":[{"tool":"test_emit","args":{"text":` + string(source) + `}},` + stage + `]}`
			result, err := NewPipelineTool(manager).Execute(context.Background(), json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			if tc.errorText != "" {
				if result.Success || !strings.Contains(result.Error, tc.errorText) {
					t.Fatalf("expected %q failure, got %+v", tc.errorText, result)
				}
				return
			}
			if !result.Success {
				t.Fatalf("failed: %s", result.Error)
			}
			var got, want interface{}
			if err := json.Unmarshal([]byte(result.Output), &got); err != nil {
				t.Fatalf("invalid output %q: %v", result.Output, err)
			}
			_ = json.Unmarshal([]byte(tc.want), &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

func TestPipelineTool_PerItemSafety(t *testing.T) {
	manager := NewManager(t.TempDir())
	manager.Register(&emitTool{})
	manager.Register(&failTool{})
	for _, tc := range []struct{ name, raw, errorText string }{
		{"first stage", `{"steps":[{"tool":"test_emit","per_item":true}]}`, "previous"},
		{"predicate without per item", `{"steps":[{"tool":"test_emit","keep_if":{"field":"answer","op":"eq","value":"include"}}]}`, "per_item"},
		{"failed item", `{"steps":[{"tool":"test_emit","args":{"text":"a"}},{"tool":"test_fail","per_item":true}]}`, "boom"},
		{"stateful tool", `{"steps":[{"tool":"test_emit","args":{"text":"a"}},{"tool":"browser_chrome","per_item":true}]}`, "stateful"},
		{"default bound", `{"steps":[{"tool":"test_emit","args":{"text":"1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13"}},{"tool":"test_fail","per_item":true}]}`, "too many"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := NewPipelineTool(manager).Execute(context.Background(), json.RawMessage(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || !strings.Contains(result.Error, tc.errorText) {
				t.Fatalf("expected %q failure, got %+v", tc.errorText, result)
			}
		})
	}
}

func TestPipelineTool_PerItemSearchPaths(t *testing.T) {
	for _, tc := range []struct{ tool, output, mode string }{
		{"find_files", "[slug]/page.go\nPage file.go\n spaced.go \n\n(showing all 3 files)", "files"},
		{"find_files", "[slug]/page.go\nPage file.go\n spaced.go \n\nPage 1 of 2+ (showing 1-3 of 4+ files)\nUse page=2 for next page", "files"},
		{"grep", "[slug]/page.go\nPage file.go\n spaced.go ", "files"},
		{"grep", "[slug]/page.go\nPage file.go\n spaced.go ", " FILES "},
	} {
		t.Run(tc.tool+tc.mode+tc.output, func(t *testing.T) {
			root := t.TempDir()
			manager := NewManager(root)
			manager.Register(&emitTool{})
			manager.Register(&pipelineProbeTool{name: tc.tool, run: func(_ context.Context, raw json.RawMessage) (*Result, error) {
				var args map[string]interface{}
				_ = json.Unmarshal(raw, &args)
				if args["path"] != "src" {
					t.Errorf("search root: %v", args["path"])
				}
				return &Result{Success: true, Output: tc.output}, nil
			}})
			manager.Register(&pipelineProbeTool{name: "test_path", run: func(_ context.Context, raw json.RawMessage) (*Result, error) {
				var args map[string]string
				_ = json.Unmarshal(raw, &args)
				return &Result{Success: true, Output: args["path"]}, nil
			}})
			raw := `{"steps":[{"tool":"test_emit","args":{"text":"src"}},{"tool":"` + tc.tool + `","args":{"mode":"` + tc.mode + `"},"input_key":"path"},{"tool":"test_path","per_item":true,"input_key":"path"}]}`
			result, err := NewPipelineTool(manager).Execute(context.Background(), json.RawMessage(raw))
			if err != nil || !result.Success {
				t.Fatalf("%v %+v", err, result)
			}
			var got []string
			if err := json.Unmarshal([]byte(result.Output), &got); err != nil {
				t.Fatal(err)
			}
			want := []string{filepath.Join(root, "src", "[slug]/page.go"), filepath.Join(root, "src", "Page file.go"), filepath.Join(root, "src", " spaced.go ")}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v want %v", got, want)
			}
		})
	}
}

func TestPipelineTool_PerItemOutputSafety(t *testing.T) {
	manager := NewManager(t.TempDir())
	manager.Register(&emitTool{})
	manager.Register(&pipelineProbeTool{name: "test_verdict", run: func(_ context.Context, _ json.RawMessage) (*Result, error) {
		return &Result{Success: true, Output: `{"answer":"include","padding":"` + strings.Repeat("x", 20000) + `"}`}, nil
	}})
	manager.Register(&pipelineProbeTool{name: "test_cached", run: func(_ context.Context, _ json.RawMessage) (*Result, error) {
		return &Result{Success: true, Output: "stub", Metadata: map[string]interface{}{"read_cache_reference": map[string]interface{}{"stub": "stub", "body": "original body"}, "tag": "retained"}}, nil
	}})
	for _, tc := range []struct {
		name, stage, want string
		truncated         bool
	}{
		{"lossless gate", `{"tool":"test_verdict","per_item":true,"keep_if":{"field":"answer","op":"eq","value":"include"}}`, `["a","b"]`, false},
		{"JSON truncation", `{"tool":"test_emit","per_item":true,"args":{"text":"0123456789"}}`, `["012\n... (output truncated)","012\n... (output truncated)"]`, true},
		{"read cache body", `{"tool":"test_cached","per_item":true}`, `["original body","original body"]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := 6
			if tc.name == "read cache body" {
				cap = 100
			}
			raw := fmt.Sprintf(`{"max_output_chars":%d,"steps":[{"tool":"test_emit","args":{"text":"a\nb"}},%s]}`, cap, tc.stage)
			result, err := NewPipelineTool(manager).Execute(context.Background(), json.RawMessage(raw))
			if err != nil || !result.Success {
				t.Fatalf("%v %+v", err, result)
			}
			if result.Output != tc.want {
				t.Fatalf("got %q want %q", result.Output, tc.want)
			}
			if result.Metadata["final_output_truncated"] != tc.truncated {
				t.Fatalf("metadata: %v", result.Metadata)
			}
			if tc.name == "read cache body" {
				items, ok := result.Metadata["item_metadata"].([]map[string]interface{})
				if !ok || len(items) != 2 || items[0]["tag"] != "retained" {
					t.Fatalf("lost metadata: %v", result.Metadata)
				}
			}
		})
	}
}

func TestPipelineTool_PerItemConcurrentOrdered(t *testing.T) {
	manager := NewManager(t.TempDir())
	manager.Register(&emitTool{})
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	manager.Register(&pipelineProbeTool{name: "test_barrier", run: func(ctx context.Context, raw json.RawMessage) (*Result, error) {
		var args map[string]string
		_ = json.Unmarshal(raw, &args)
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if args["input"] == "a" {
			time.Sleep(10 * time.Millisecond)
		}
		return &Result{Success: true, Output: args["prefix"] + args["input"]}, nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan *Result, 1)
	go func() {
		result, _ := NewPipelineTool(manager).Execute(ctx, json.RawMessage(`{"steps":[{"tool":"test_emit","args":{"text":"a\nb"}},{"tool":"test_barrier","per_item":true,"args":{"prefix":"x"}}]}`))
		done <- result
	}()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("items did not execute concurrently")
		}
	}
	close(release)
	result := <-done
	if result == nil || !result.Success || result.Output != `["xa","xb"]` {
		t.Fatalf("unexpected result: %+v", result)
	}
}
