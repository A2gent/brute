package contextcompress

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestCompressorDisabledPassesThrough(t *testing.T) {
	req := &llm.ChatRequest{
		Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{ToolCallID: "call-1", Name: "bash", Content: strings.Repeat("x", 9000)}}}},
		Tools:    []llm.ToolDefinition{{Name: "bash"}},
	}
	compressed, result := NewCompressor(Config{Enabled: false}).CompressRequest(context.Background(), "sess-1", req)

	if result.Applied {
		t.Fatalf("expected no compression when disabled")
	}
	if compressed.Messages[0].ToolResults[0].Content != req.Messages[0].ToolResults[0].Content {
		t.Fatalf("disabled compressor changed tool content")
	}
	if len(compressed.Tools) != 1 || compressed.Tools[0].Name != "bash" {
		t.Fatalf("disabled compressor changed tools: %+v", compressed.Tools)
	}
}

func TestCompressorCompressesLargeBashOutputAndPreservesErrors(t *testing.T) {
	content := largeLogOutput()
	req := &llm.ChatRequest{
		SystemPrompt: "base prompt",
		Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{
			ToolCallID: "call-1",
			Name:       "bash",
			Content:    content,
			IsError:    true,
		}}}},
		Tools: []llm.ToolDefinition{{Name: "bash"}},
	}

	compressed, result := NewCompressor(Config{Enabled: true}).CompressRequest(context.Background(), "sess-1", req)
	if !result.Applied {
		t.Fatalf("expected compression to apply")
	}
	got := compressed.Messages[0].ToolResults[0].Content
	if len(got) >= len(content) {
		t.Fatalf("expected compressed content to be smaller, got original=%d compressed=%d", len(content), len(got))
	}
	for _, want := range []string{"brute-compressed", "FAIL: TestImportantThing", "panic: cannot open database", "context_retrieve"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compressed content missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(compressed.SystemPrompt, "context_retrieve") {
		t.Fatalf("expected system prompt retrieval instructions, got %q", compressed.SystemPrompt)
	}
	if !hasTool(compressed.Tools, RetrievalToolName) {
		t.Fatalf("expected retrieval tool to be injected, got %+v", compressed.Tools)
	}
	if compressed.Messages[0].ToolResults[0].ToolCallID != "call-1" {
		t.Fatalf("tool call ID was not preserved")
	}
	if !compressed.Messages[0].ToolResults[0].IsError {
		t.Fatalf("expected error flag to be preserved")
	}
}

func TestCompressorCompressesSearchOutputAndPreservesPathsAndLineNumbers(t *testing.T) {
	content := largeSearchOutput()
	req := &llm.ChatRequest{
		Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{
			ToolCallID: "grep-1",
			Name:       "grep",
			Content:    content,
		}}}},
		Tools: []llm.ToolDefinition{{Name: "grep"}},
	}

	compressed, result := NewCompressor(Config{Enabled: true}).CompressRequest(context.Background(), "sess-1", req)
	if !result.Applied {
		t.Fatalf("expected search output to be compressed")
	}
	got := compressed.Messages[0].ToolResults[0].Content
	for _, want := range []string{
		"src/pkg/file00.go:1:needle match 00-01",
		"src/pkg/file00.go:40:needle match 00-40",
		"src/pkg/file07.go:1:needle match 07-01",
		"src/pkg/file07.go:40:needle match 07-40",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compressed search output missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "lines omitted") {
		t.Fatalf("expected omission marker in compressed search output:\n%s", got)
	}
}

func TestCompressorDoesNotCompressExcludedEditingTools(t *testing.T) {
	content := strings.Repeat("exact file content\n", 1000)
	for _, toolName := range []string{"read", "write", "edit"} {
		t.Run(toolName, func(t *testing.T) {
			req := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{
				ToolCallID: toolName + "-1",
				Name:       toolName,
				Content:    content,
			}}}}}

			compressed, result := NewCompressor(Config{Enabled: true}).CompressRequest(context.Background(), "sess-1", req)
			if result.Applied {
				t.Fatalf("expected %s output to stay exact", toolName)
			}
			if compressed.Messages[0].ToolResults[0].Content != content {
				t.Fatalf("%s content changed", toolName)
			}
		})
	}
}

func TestCompressorRetrieveReturnsOriginalAndQueryMatches(t *testing.T) {
	content := largeQueryableOutput()
	compressor := NewCompressor(Config{Enabled: true})
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{
		ToolCallID: "call-1",
		Name:       "bash",
		Content:    content,
	}}}}}
	_, result := compressor.CompressRequest(context.Background(), "sess-1", req)
	if !result.Applied || len(result.Items) != 1 {
		t.Fatalf("expected one compressed item, got %+v", result)
	}

	full, ok := compressor.Retrieve("sess-1", result.Items[0].Hash, "")
	if !ok {
		t.Fatalf("expected retrieval to succeed")
	}
	if full != content {
		t.Fatalf("full retrieval mismatch:\n%s", full)
	}

	matches, ok := compressor.Retrieve("sess-1", result.Items[0].Hash, "target")
	if !ok {
		t.Fatalf("expected query retrieval to succeed")
	}
	if strings.Contains(matches, "alpha first") || !strings.Contains(matches, "beta target one") || !strings.Contains(matches, "beta target two") {
		t.Fatalf("query retrieval returned unexpected content:\n%s", matches)
	}
}

func TestCompressorRetrieveFallsBackToSessionStorage(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	sess, err := sessionManager.Create("test-agent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	content := largeQueryableOutput()
	sess.AddToolResult([]session.ToolResult{{
		ToolCallID: "call-1",
		Name:       "bash",
		Content:    content,
	}})
	if err := sessionManager.Save(sess); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	compressor := NewCompressorWithSessionStore(Config{Enabled: true}, sessionManager)
	_, result := compressor.CompressRequest(context.Background(), sess.ID, &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{
		ToolCallID: "call-1",
		Name:       "bash",
		Content:    content,
	}}}}})
	if !result.Applied || len(result.Items) != 1 {
		t.Fatalf("expected one compressed item, got %+v", result)
	}

	freshCompressor := NewCompressorWithSessionStore(Config{Enabled: true}, sessionManager)
	full, ok := freshCompressor.Retrieve(sess.ID, result.Items[0].Hash, "")
	if !ok {
		t.Fatalf("expected retrieval from persisted session storage to succeed")
	}
	if full != content {
		t.Fatalf("persisted retrieval mismatch:\n%s", full)
	}

	matches, ok := freshCompressor.Retrieve(sess.ID, result.Items[0].Hash, "target")
	if !ok || !strings.Contains(matches, "beta target one") || !strings.Contains(matches, "beta target two") {
		t.Fatalf("expected query retrieval from session storage, got ok=%v content=%q", ok, matches)
	}
}

func hasTool(tools []llm.ToolDefinition, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func largeLogOutput() string {
	return strings.Join([]string{
		"running tests",
		strings.Repeat("noise line\n", 1400),
		"FAIL: TestImportantThing",
		"panic: cannot open database",
		"exit status 1",
	}, "\n")
}

func largeSearchOutput() string {
	lines := []string{"search results"}
	for fileIdx := 0; fileIdx < 8; fileIdx++ {
		for lineNo := 1; lineNo <= 40; lineNo++ {
			lines = append(lines, fmt.Sprintf("src/pkg/file%02d.go:%d:needle match %02d-%02d", fileIdx, lineNo, fileIdx, lineNo))
		}
	}
	return strings.Join(lines, "\n")
}

func largeQueryableOutput() string {
	lines := []string{"alpha first"}
	for i := 0; i < 900; i++ {
		lines = append(lines, fmt.Sprintf("noise line %04d", i))
	}
	lines = append(lines,
		"beta target one",
		"gamma middle",
		"beta target two",
	)
	return strings.Join(lines, "\n")
}
