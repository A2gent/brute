package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelevanceGatePipelinePreservesFullFallbackAndSkips(t *testing.T) {
	dir := t.TempDir()
	full := strings.Repeat("full content\n", 30000)
	relevanceGateFile(t, dir, "large.txt", full)
	manager := NewManager(dir)
	manager.Register(NewRelevanceGateTool(dir, nil))
	raw, _ := json.Marshal(map[string]any{"steps": []map[string]any{{"tool": "relevance_gate", "args": map[string]any{"paths": []string{"large.txt", ".env"}, "task_context": "Inspect"}}}, "max_output_chars": 10})
	result, err := NewPipelineTool(manager).Execute(context.Background(), raw)
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var verdicts []relevanceGateVerdict
	if err := json.Unmarshal([]byte(result.Output), &verdicts); err != nil {
		t.Fatalf("pipeline truncated verdicts: %v", err)
	}
	if len(verdicts) != 2 || verdicts[0].Content != full || verdicts[1].Path != ".env" || verdicts[1].Action != "skip" {
		t.Fatal("lost full fallback or skip")
	}
}

func TestRelevanceGateNativeSearchPipeline(t *testing.T) {
	dir := t.TempDir()
	relevanceGateFile(t, dir, "candidate.txt", "parser needle\nparser needle\n")
	relevanceGateFile(t, dir, ".env", "parser secret\n")
	for _, name := range []string{"file_search", "content_search"} {
		hit := "candidate"
		if name == "content_search" {
			hit = "parser needle"
		}
		for _, query := range []string{hit, "nothing-matches-here"} {
			enabled := true
			manager := NewManagerWithOptions(dir, &ManagerOptions{FileIndexingEnabled: &enabled})
			manager.Register(NewRelevanceGateTool(dir, nil))
			raw, _ := json.Marshal(map[string]any{"steps": []map[string]any{{"tool": name, "args": map[string]any{"query": query}}, {"tool": "relevance_gate", "input_from_prev": true, "args": map[string]any{"task_context": "Parser"}}}})
			result, err := NewPipelineTool(manager).Execute(context.Background(), raw)
			if err != nil || !result.Success {
				t.Fatalf("%s: result=%+v err=%v", name, result, err)
			}
			var verdicts []relevanceGateVerdict
			if err := json.Unmarshal([]byte(result.Output), &verdicts); err != nil {
				t.Fatal(err)
			}
			if query == hit && (len(verdicts) < 1 || verdicts[0].Content != "parser needle\nparser needle\n") {
				t.Fatalf("native search: %+v", verdicts)
			}
		}
	}
	client, requests := relevanceGateServer(t, 200, `{"answers":{}}`)
	verdicts := relevanceGateExecute(t, NewRelevanceGateTool(dir, client), map[string]any{"input": "candidate.txt:1: parser needle\n.env:1: NEVER-SEND-SNIPPET", "task_context": "Parser"})
	if len(verdicts) != 2 || verdicts[1].Action != "skip" {
		t.Fatalf("native content search %+v", verdicts)
	}
	for _, request := range requests() {
		if strings.Contains(request.Raw, "NEVER-SEND-SNIPPET") || strings.Contains(request.Raw, ".env") {
			t.Fatal("native snippet leaked")
		}
	}
}

func TestRelevanceGateSecureOpenRejectsReplacedSymlinks(t *testing.T) {
	dir := t.TempDir()
	relevanceGateFile(t, dir, ".env", "private")
	relevanceGateFile(t, dir, "keys/private.txt", "private")
	if err := os.Symlink(filepath.Join(dir, ".env"), filepath.Join(dir, "replaced.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "keys"), filepath.Join(dir, "replaced-parent")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"replaced.txt", "replaced-parent/private.txt"} {
		file, err := openRelevanceFile(filepath.Join(dir, path))
		if file != nil {
			file.Close()
		}
		if err == nil {
			t.Fatalf("secure open followed replaced symlink %s", path)
		}
	}
}

func TestRelevanceGateScopedAndBracketSearch(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".relevance-scoped-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	relevanceGateFile(t, dir, "[id].txt", "WRONG root")
	relevanceGateFile(t, dir, "src/[id].txt", "RIGHT parser needle")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	for _, name := range []string{"file_search", "content_search"} {
		manager := NewManagerWithOptions(relative, &ManagerOptions{FileIndexingEnabled: &enabled})
		manager.Register(NewRelevanceGateTool(relative, nil))
		query := "[id]"
		if name == "content_search" {
			query = "needle"
		}
		raw, _ := json.Marshal(map[string]any{"steps": []map[string]any{{"tool": name, "args": map[string]any{"path": "src", "query": query}}, {"tool": "relevance_gate", "input_from_prev": true, "args": map[string]any{"task_context": "Parser"}}}})
		result, err := NewPipelineTool(manager).Execute(context.Background(), raw)
		if err != nil || !result.Success {
			t.Fatalf("%s result=%+v err=%v", name, result, err)
		}
		var verdicts []relevanceGateVerdict
		if err := json.Unmarshal([]byte(result.Output), &verdicts); err != nil {
			t.Fatal(err)
		}
		if len(verdicts) != 1 || verdicts[0].Content != "RIGHT parser needle" {
			t.Fatalf("scoped search read wrong file: %+v", verdicts)
		}
	}
}

func TestRelevanceGateRelativeWorkDir(t *testing.T) {
	dir := t.TempDir()
	relevanceGateFile(t, dir, "public.txt", "full public file")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	verdicts := relevanceGateExecute(t, NewRelevanceGateTool(relative, nil), map[string]any{"paths": []string{"public.txt"}, "task_context": "Inspect"})
	if verdicts[0].Error != "" || verdicts[0].Content != "full public file" {
		t.Fatalf("relative workspace: %+v", verdicts)
	}
}
