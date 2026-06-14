package agent

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/contextcompress"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

func TestBuildRequestPassesThroughToolResultsWhenCompressionDisabled(t *testing.T) {
	sess := session.New("test-agent")
	sess.AddAssistantMessage("", []session.ToolCall{{ID: "tc-bash", Name: "bash", Input: []byte(`{"command":"go test"}`)}})
	original := strings.Repeat("verbatim\n", 900)
	sess.AddToolResult([]session.ToolResult{{
		ToolCallID: "tc-bash",
		Name:       "bash",
		Content:    original,
		IsError:    true,
		Metadata:   map[string]interface{}{"source": "test"},
		DurationMs: 321,
	}})

	ag := NewWithCompressor(
		Config{CompressToolResults: false},
		nil,
		tools.NewManager(t.TempDir()),
		nil,
		contextcompress.NewCompressor(contextcompress.Config{Enabled: true}),
	)
	request := ag.buildRequest(sess)
	if len(request.Messages) != 2 {
		t.Fatalf("expected two messages, got %d", len(request.Messages))
	}
	got := request.Messages[1].ToolResults[0]
	if got.Content != original {
		t.Fatalf("expected passthrough tool result content, got %q", got.Content)
	}
	if !got.IsError || got.ToolCallID != "tc-bash" || got.Name != "bash" || got.DurationMs != 321 {
		t.Fatalf("unexpected passthrough tool result: %+v", got)
	}
	if got.Metadata["source"] != "test" {
		t.Fatalf("expected metadata to be preserved, got %+v", got.Metadata)
	}
}

func TestBuildRequestCompressesToolResultsWhenEnabled(t *testing.T) {
	sess := session.New("test-agent")
	sess.AddAssistantMessage("", []session.ToolCall{{ID: "tc-bash", Name: "bash", Input: []byte(`{"command":"go test"}`)}})
	sess.AddToolResult([]session.ToolResult{{
		ToolCallID: "tc-bash",
		Name:       "bash",
		Content:    strings.Repeat("noise\n", 1200) + "FAIL: important test\nexit status 1\n",
	}})

	manager := tools.NewManager(t.TempDir())
	compressor := contextcompress.NewCompressor(contextcompress.Config{Enabled: true})
	manager.Register(contextcompress.NewRetrieveTool(compressor))
	ag := NewWithCompressor(Config{CompressToolResults: true}, nil, manager, nil, compressor)

	request := ag.buildRequest(sess)
	if len(request.Messages) != 2 {
		t.Fatalf("expected two messages, got %d", len(request.Messages))
	}
	got := request.Messages[1].ToolResults[0].Content
	if !strings.Contains(got, "brute-compressed") {
		t.Fatalf("expected compressed marker, got %q", got)
	}
	if !strings.Contains(got, "FAIL: important test") {
		t.Fatalf("expected important failure line to be preserved, got %q", got)
	}
	if !strings.Contains(request.SystemPrompt, "context_retrieve") {
		t.Fatalf("expected retrieval instructions in system prompt")
	}
}

func TestBuildRequestCompressionPreservesToolResultStructureAndOrder(t *testing.T) {
	sess := session.New("test-agent")
	sess.AddAssistantMessage("", []session.ToolCall{
		{ID: "tc-1", Name: "grep", Input: []byte(`{"pattern":"hit","include":"*.go"}`)},
		{ID: "tc-2", Name: "read", Input: []byte(`{"path":"file.txt"}`)},
	})
	sess.AddToolResult([]session.ToolResult{
		{
			ToolCallID: "tc-1",
			Name:       "grep",
			Content:    strings.Repeat("src/a.go:10:hit\n", 1200),
			Metadata:   map[string]interface{}{"kind": "search"},
			DurationMs: 77,
		},
		{
			ToolCallID: "tc-2",
			Name:       "read",
			Content:    strings.Repeat("exact\n", 1200),
			Metadata:   map[string]interface{}{"kind": "file"},
			DurationMs: 88,
		},
	})

	compressor := contextcompress.NewCompressor(contextcompress.Config{Enabled: true})
	ag := NewWithCompressor(Config{CompressToolResults: true}, nil, tools.NewManager(t.TempDir()), nil, compressor)
	request := ag.buildRequest(sess)
	if len(request.Messages) != 2 {
		t.Fatalf("expected two messages, got %d", len(request.Messages))
	}
	results := request.Messages[1].ToolResults
	if len(results) != 2 {
		t.Fatalf("expected two tool results, got %d (message=%+v)", len(results), request.Messages[1])
	}
	if results[0].ToolCallID != "tc-1" || results[1].ToolCallID != "tc-2" {
		t.Fatalf("tool result order/call IDs changed: %+v", results)
	}
	if results[0].Name != "grep" || results[1].Name != "read" {
		t.Fatalf("tool result names changed: %+v", results)
	}
	if results[0].DurationMs != 77 || results[1].DurationMs != 88 {
		t.Fatalf("tool result durations changed: %+v", results)
	}
	if results[0].Metadata["kind"] != "search" || results[1].Metadata["kind"] != "file" {
		t.Fatalf("tool result metadata changed: %+v", results)
	}
	if !strings.Contains(results[0].Content, "brute-compressed") {
		t.Fatalf("expected first result to be compressed, got %q", results[0].Content)
	}
	if results[1].Content != strings.Repeat("exact\n", 1200) {
		t.Fatalf("expected excluded tool result to remain exact")
	}
}

func TestNewWithCompressorEnablesCompressionFromEnv(t *testing.T) {
	t.Setenv(envCompressToolResults, "true")
	compressor := contextcompress.NewCompressor(contextcompress.Config{Enabled: true})
	ag := NewWithCompressor(Config{}, nil, tools.NewManager(t.TempDir()), nil, compressor)
	if !ag.config.CompressToolResults {
		t.Fatalf("expected env to enable tool result compression")
	}
}

func TestBuildRequestProviderStructureRemainsValidForToolResults(t *testing.T) {
	sess := session.New("test-agent")
	sess.AddAssistantMessage("", []session.ToolCall{{ID: "call_1", Name: "bash", Input: []byte(`{"command":"echo hi"}`)}})
	sess.AddToolResult([]session.ToolResult{{
		ToolCallID: "call_1",
		Name:       "bash",
		Content:    strings.Repeat("noise\n", 1200) + "panic: boom\n",
		Metadata:   map[string]interface{}{"trace": "kept"},
		DurationMs: 55,
	}})

	ag := NewWithCompressor(
		Config{CompressToolResults: true},
		nil,
		tools.NewManager(t.TempDir()),
		nil,
		contextcompress.NewCompressor(contextcompress.Config{Enabled: true}),
	)
	request := ag.buildRequest(sess)
	if len(request.Messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(request.Messages))
	}
	tr := request.Messages[1].ToolResults[0]
	if tr.ToolCallID != "call_1" {
		t.Fatalf("tool call id = %q, want call_1", tr.ToolCallID)
	}
	if tr.Name != "bash" {
		t.Fatalf("tool name = %q, want bash", tr.Name)
	}
	if tr.DurationMs != 55 {
		t.Fatalf("duration = %d, want 55", tr.DurationMs)
	}
	if tr.Metadata["trace"] != "kept" {
		t.Fatalf("metadata changed: %+v", tr.Metadata)
	}
	if !strings.Contains(tr.Content, "brute-compressed") {
		t.Fatalf("expected compressed marker, got %q", tr.Content)
	}
}
