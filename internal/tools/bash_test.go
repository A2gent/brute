package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestBashFullOutputAndExitCode(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	raw := json.RawMessage(`{"command":"printf 'HEAD\\n'; for ((i=0;i<6000;i++)); do printf 'progress line\\n'; done; printf 'TAIL\\n'; printf 'compiler diagnostic\\n' >&2; exit 7"}`)
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}
	for _, want := range []string{"HEAD", "TAIL", "compiler diagnostic", "Exit code: 7"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("lost %q", want)
		}
	}
	if strings.Contains(result.Output, "output truncated") {
		t.Fatal("destructive truncation")
	}
	if strings.Count(result.Output, "progress line") != 6000 {
		t.Fatal("raw output must stay complete")
	}
}

func TestManagerKeepsFailedBashOutput(t *testing.T) {
	m := NewManager(t.TempDir())
	results := m.ExecuteParallel(context.Background(), []llm.ToolCall{{ID: "b", Name: "bash", Input: `{"command":"printf 'ERROR build broke\\n'; exit 3"}`}})
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("results: %+v", results)
	}
	for _, want := range []string{"ERROR build broke", "Exit code: 3", "exit status 3"} {
		if !strings.Contains(results[0].Content, want) {
			t.Fatalf("lost %q in %s", want, results[0].Content)
		}
	}
}

func TestBashWrappersKeepOriginal(t *testing.T) {
	m := NewManager(t.TempDir())
	args := map[string]interface{}{"command": "for ((i=0;i<6000;i++)); do printf 'progress line\\n'; done; printf 'ERROR middle\\n'; printf 'tail\\n'"}
	for _, name := range []string{"parallel", "pipeline"} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]interface{}{"steps": []map[string]interface{}{{"tool": "bash", "args": args}}, "max_output_chars": 100})
			result, err := m.Execute(context.Background(), name, raw)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Output, "ERROR middle") {
				t.Fatal("wrapper lost diagnostic")
			}
			if strings.Count(result.Output, "progress line") != 6000 {
				t.Fatal("wrapper lost original")
			}
		})
	}
}

func TestBashNestedPipelineParallelPreservesOriginal(t *testing.T) {
	for _, exit := range []string{"0", "7"} {
		t.Run(exit, func(t *testing.T) {
			m := NewManager(t.TempDir())
			inner := map[string]interface{}{"steps": []map[string]interface{}{{"tool": "bash", "args": map[string]interface{}{"command": "for ((i=0;i<6000;i++)); do printf 'progress line\\n'; done; printf 'ERROR nested\\n'; exit " + exit}}}, "max_output_chars": 100}
			raw, _ := json.Marshal(map[string]interface{}{"steps": []map[string]interface{}{{"tool": "parallel", "args": inner}}, "max_output_chars": 100})
			result, err := m.Execute(context.Background(), "pipeline", raw)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(result.Output, "progress line") != 6000 || !strings.Contains(result.Output, "ERROR nested") {
				t.Fatal("nested original lost")
			}
			if result.Metadata["command_output_kind"] != "parallel" {
				t.Fatal("missing shape metadata")
			}
		})
	}
}

func TestBashExitStates(t *testing.T) {
	for _, tc := range []struct {
		command string
		timeout int
		exit    int
	}{{"printf 'ready'", 0, 0}, {"kill -TERM $$", 0, -1}, {"while :; do :; done", 20, -1}} {
		raw, _ := json.Marshal(map[string]interface{}{"command": tc.command, "timeout": tc.timeout})
		result, err := NewBashTool(t.TempDir()).Execute(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		if result.Metadata["exit_code"] != tc.exit {
			t.Fatalf("command %q metadata: %+v", tc.command, result.Metadata)
		}
	}
}
