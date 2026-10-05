package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestReadTool_LineNumberFormatting(t *testing.T) {
	workDir := t.TempDir()
	path := filepath.Join(workDir, "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatalf("failed to create sample file: %v", err)
	}

	tool := NewReadTool(workDir)

	t.Run("line numbers disabled by default", func(t *testing.T) {
		params := map[string]interface{}{
			"path":  "sample.txt",
			"limit": 2,
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if strings.Contains(result.Output, "\talpha") || strings.Contains(result.Output, "\tbeta") {
			t.Fatalf("expected output without line numbers, got: %q", result.Output)
		}
		if !strings.Contains(result.Output, "alpha\nbeta") {
			t.Fatalf("expected plain content lines, got: %q", result.Output)
		}
	})

	t.Run("line numbers can be enabled explicitly", func(t *testing.T) {
		params := map[string]interface{}{
			"path":                 "sample.txt",
			"limit":                2,
			"include_line_numbers": true,
		}
		raw, _ := json.Marshal(params)
		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if !strings.Contains(result.Output, "     1\talpha") {
			t.Fatalf("expected line-numbered output for line 1, got: %q", result.Output)
		}
		if !strings.Contains(result.Output, "     2\tbeta") {
			t.Fatalf("expected line-numbered output for line 2, got: %q", result.Output)
		}
	})
}

func TestReadTool_ResolvesSingleNestedGitProject(t *testing.T) {
	workDir := t.TempDir()
	nestedRoot := filepath.Join(workDir, "spareto")
	if err := os.MkdirAll(filepath.Join(nestedRoot, ".git"), 0755); err != nil {
		t.Fatalf("failed to create nested git dir: %v", err)
	}
	path := filepath.Join(nestedRoot, "app/services/clickstack/metrics.rb")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create nested dirs: %v", err)
	}
	if err := os.WriteFile(path, []byte("nested metrics\n"), 0644); err != nil {
		t.Fatalf("failed to create nested file: %v", err)
	}

	tool := NewReadTool(workDir)
	raw, _ := json.Marshal(map[string]interface{}{
		"path": "app/services/clickstack/metrics.rb",
	})
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "nested metrics") {
		t.Fatalf("expected nested file content, got: %q", result.Output)
	}
}

// A cache hit is safe only after the full result has reached the model request.
func TestReadTool_SessionCache(t *testing.T) {
	for _, tc := range []struct {
		name     string
		params   string
		change   string
		wantStub bool
	}{
		{"repeat", `{"path":"sample.txt","limit":2}`, "", true},
		{"changed file", `{"path":"sample.txt","limit":2}`, "alpha\nbeta\nchanged outside range\n", false},
		{"different range", `{"path":"sample.txt","offset":1,"limit":2}`, "", false},
		{"force", `{"path":"sample.txt","limit":2,"force":true}`, "", false},
		{"different formatting", `{"path":"sample.txt","limit":2,"include_line_numbers":true}`, "", false},
		{"equivalent range", `{"path":"./sample.txt","start_line":1,"end_line":2}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "sample.txt")
			if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
				t.Fatal(err)
			}
			tool := NewReadTool(dir)
			ctx := context.WithValue(context.Background(), "session_id", "session-a")
			ctx = context.WithValue(ctx, "tool_call_id", "original-read")
			first, err := tool.Execute(ctx, json.RawMessage(`{"path":"sample.txt","limit":2}`))
			if err != nil || !first.Success || !strings.Contains(first.Output, "alpha\nbeta") {
				t.Fatalf("first read: %+v, %v", first, err)
			}
			tool.SyncContext("session-a", []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{ToolCallID: "original-read", Name: "read", Content: first.Output}}}})
			if tc.change != "" {
				if err := os.WriteFile(path, []byte(tc.change), 0644); err != nil {
					t.Fatal(err)
				}
			}
			result, err := tool.Execute(ctx, json.RawMessage(tc.params))
			if err != nil || !result.Success {
				t.Fatalf("repeat read: %+v, %v", result, err)
			}
			stub := strings.Contains(result.Output, "unchanged since earlier read in this session")
			if stub != tc.wantStub {
				t.Fatalf("stub = %v, want %v: %s", stub, tc.wantStub, result.Output)
			}
			if stub {
				for _, text := range []string{"2 lines", "earlier in the conversation", "original-read", "force: true"} {
					if !strings.Contains(result.Output, text) {
						t.Errorf("stub missing %q: %s", text, result.Output)
					}
				}
			}
		})
	}
}

func TestReadTool_CacheRequiresVisibleOriginal(t *testing.T) {
	for _, mode := range []string{"pending", "dropped", "compressed", "parallel full", "parallel truncated", "different session", "no session", "pipeline"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("alpha\nbeta\n"), 0644); err != nil {
				t.Fatal(err)
			}
			tool := NewReadTool(dir)
			ctx := context.WithValue(context.Background(), "session_id", "session-a")
			ctx = context.WithValue(ctx, "tool_call_id", "original-read")
			if mode == "no session" {
				ctx = context.Background()
			}
			if mode == "pipeline" {
				ctx = context.WithValue(ctx, pipelineContextKey{}, 1)
			}
			params := json.RawMessage(`{"path":"sample.txt"}`)
			first, err := tool.Execute(ctx, params)
			if err != nil {
				t.Fatal(err)
			}
			original := llm.ToolResult{ToolCallID: "original-read", Name: "read", Content: first.Output}
			messages := []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{original}}}
			if mode != "pending" {
				tool.SyncContext("session-a", messages)
			}
			switch mode {
			case "dropped":
				tool.SyncContext("session-a", nil)
			case "compressed":
				messages[0].ToolResults[0].Content = "[brute-compressed] alpha"
				tool.SyncContext("session-a", messages)
			case "parallel full", "parallel truncated":
				output := first.Output
				if mode == "parallel truncated" {
					output = "alpha... (output truncated)"
				}
				raw, _ := json.Marshal([]parallelStepOutput{{Tool: "read", Success: true, Output: output}})
				messages[0].ToolResults[0].Name = "parallel"
				messages[0].ToolResults[0].Content = string(raw)
				tool.SyncContext("session-a", messages)
			case "different session":
				ctx = context.WithValue(ctx, "session_id", "session-b")
			}
			result, err := tool.Execute(ctx, params)
			if err != nil {
				t.Fatal(err)
			}
			stub := strings.Contains(result.Output, "unchanged since earlier read")
			if stub != (mode == "parallel full") {
				t.Fatalf("unexpected cache result: %s", result.Output)
			}
		})
	}
}

func TestReadTool_ParallelAndPipeline(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprint("truncated=", truncated), func(t *testing.T) {
			dir := t.TempDir()
			body := strings.Repeat("alpha beta gamma\n", 100)
			if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(dir)
			tool, _ := manager.Get("read")
			cache := tool.(*ReadTool)
			ctx := context.WithValue(context.Background(), "session_id", "session-a")
			ctx = context.WithValue(ctx, "tool_call_id", "original-parallel")
			maxChars := 12000
			if truncated {
				maxChars = 10
			}
			raw, _ := json.Marshal(map[string]interface{}{"steps": []map[string]interface{}{{"tool": "read", "args": map[string]interface{}{"path": "sample.txt", "limit": 100}}}, "max_output_chars": maxChars})
			first, err := manager.Execute(ctx, "parallel", raw)
			if err != nil || !first.Success {
				t.Fatalf("parallel: %+v %v", first, err)
			}
			messages := []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{ToolCallID: "original-parallel", Name: "parallel", Content: first.Output, Metadata: first.Metadata}}}}
			cache.SyncContext("session-a", messages)
			ctx = context.WithValue(ctx, "tool_call_id", "repeat-parallel")
			repeat, err := manager.Execute(ctx, "parallel", raw)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(repeat.Output, "unchanged since earlier read") != !truncated {
				t.Fatalf("unexpected parallel output: %s", repeat.Output)
			}
			if !truncated {
				if strings.Contains(repeat.Output, "alpha beta") {
					t.Fatal("recovery body leaked into parallel JSON")
				}
				messages = []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{ToolCallID: "repeat-parallel", Name: "parallel", Content: repeat.Output, Metadata: repeat.Metadata}}}}
				cache.SyncContext("session-a", messages)
				if !strings.Contains(messages[0].ToolResults[0].Content, "alpha beta") {
					t.Fatal("dangling parallel stub not restored")
				}
			}
			// Seed a visible direct read before a real read -> filter pipeline.
			ctx = context.WithValue(ctx, "tool_call_id", "seed")
			seed, err := manager.Execute(ctx, "read", json.RawMessage(`{"path":"sample.txt","limit":100}`))
			if err != nil {
				t.Fatal(err)
			}
			cache.SyncContext("session-a", []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{ToolCallID: "seed", Name: "read", Content: seed.Output}}}})
			pipeline, err := manager.Execute(ctx, "pipeline", json.RawMessage(`{"steps":[{"tool":"read","args":{"path":"sample.txt","limit":100}},{"tool":"filter","args":{"contains":"alpha"},"input_from_prev":true}]}`))
			if err != nil || !strings.Contains(pipeline.Output, "alpha beta") {
				t.Fatalf("pipeline lost body: %+v %v", pipeline, err)
			}
		})
	}
}

func TestReadTool_EmptyAndCancelled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(dir)
	ctx := context.WithValue(context.Background(), "session_id", "session-a")
	ctx = context.WithValue(ctx, "tool_call_id", "empty")
	params := json.RawMessage(`{"path":"empty.txt"}`)
	first, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	tool.SyncContext("session-a", []llm.Message{{ToolResults: []llm.ToolResult{{ToolCallID: "empty", Name: "read", Content: first.Output}}}})
	repeat, err := tool.Execute(ctx, params)
	if err != nil || !strings.Contains(repeat.Output, "0 lines") {
		t.Fatalf("empty repeat: %+v %v", repeat, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := tool.Execute(cancelled, params); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
	result, err := tool.Execute(ctx, json.RawMessage(`{"path":"/dev/zero"}`))
	if err != nil || result.Success || !strings.Contains(result.Error, "not a regular file") {
		t.Fatalf("special file: %+v %v", result, err)
	}
}

func TestReadTool_TruncatedParallelCacheHitRecovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(dir)
	registered, _ := manager.Get("read")
	cache := registered.(*ReadTool)
	ctx := context.WithValue(context.Background(), "session_id", "session-a")
	ctx = context.WithValue(ctx, "tool_call_id", "seed")
	first, err := manager.Execute(ctx, "read", json.RawMessage(`{"path":"sample.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	cache.SyncContext("session-a", []llm.Message{{ToolResults: []llm.ToolResult{{ToolCallID: "seed", Name: "read", Content: first.Output}}}})
	ctx = context.WithValue(ctx, "tool_call_id", "repeat")
	repeat, err := manager.Execute(ctx, "parallel", json.RawMessage(`{"steps":[{"tool":"read","args":{"path":"sample.txt"}}],"max_output_chars":10}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := []llm.Message{{ToolResults: []llm.ToolResult{{ToolCallID: "repeat", Name: "parallel", Content: repeat.Output, Metadata: repeat.Metadata}}}}
	cache.SyncContext("session-a", messages)
	var steps []parallelStepOutput
	if err := json.Unmarshal([]byte(messages[0].ToolResults[0].Content), &steps); err != nil {
		t.Fatal(err)
	}
	if steps[0].Output != first.Output {
		t.Fatalf("truncated cache hit lost original: %s", steps[0].Output)
	}
}

func TestReadTool_CacheBodyLimit(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat(strings.Repeat("a", 1000)+"\n", 300)
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(dir)
	ctx := context.WithValue(context.Background(), "session_id", "session-a")
	ctx = context.WithValue(ctx, "tool_call_id", "large")
	params := json.RawMessage(`{"path":"large.txt","limit":300}`)
	first, err := tool.Execute(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	tool.SyncContext("session-a", []llm.Message{{ToolResults: []llm.ToolResult{{ToolCallID: "large", Name: "read", Content: first.Output}}}})
	repeat, err := tool.Execute(ctx, params)
	if err != nil || repeat.Output != first.Output {
		t.Fatalf("over-budget read must retain full body: %v", err)
	}
}
