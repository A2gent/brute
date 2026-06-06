package agent

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestSanitizeConversationForLLMFiltersInvalidToolCallsAndOrphanResults(t *testing.T) {
	messages := []session.Message{
		{Role: "user", Content: "inspect the file"},
		{
			Role: "assistant",
			ToolCalls: []session.ToolCall{
				{ID: "tc-valid", Name: "read", Input: []byte(`{"path":"main.go"}`)},
				{ID: "tc-array", Name: "read", Input: []byte(`[]`)},
				{ID: "", Name: "read", Input: []byte(`{"path":"missing-id.go"}`)},
				{ID: "tc-malformed", Name: "read", Input: []byte(`{"path":`)},
				{ID: "tc-blank", Name: "read", Input: []byte(``)},
			},
		},
		{
			Role: "tool",
			ToolResults: []session.ToolResult{
				{ToolCallID: "tc-valid", Name: "read", Content: "package main"},
				{ToolCallID: "tc-orphan", Name: "read", Content: "should be dropped"},
			},
		},
		{Role: "tool"},
	}

	got := sanitizeConversationForLLM(messages)
	if len(got) != 4 {
		t.Fatalf("expected 4 messages after sanitization, got %d", len(got))
	}

	if len(got[1].ToolCalls) != 1 {
		t.Fatalf("expected only one valid tool call, got %d", len(got[1].ToolCalls))
	}
	if got[1].ToolCalls[0].ID != "tc-valid" {
		t.Fatalf("expected valid tool call to be preserved, got %q", got[1].ToolCalls[0].ID)
	}

	if len(got[2].ToolResults) != 1 {
		t.Fatalf("expected only one tool result after filtering, got %d", len(got[2].ToolResults))
	}
	if got[2].ToolResults[0].ToolCallID != "tc-valid" {
		t.Fatalf("expected matching tool result to be preserved, got %q", got[2].ToolResults[0].ToolCallID)
	}

	if got[3].Role != "tool" || len(got[3].ToolResults) != 0 {
		t.Fatalf("expected tool messages without results to be preserved, got role=%q results=%d", got[3].Role, len(got[3].ToolResults))
	}
}

func TestGetActiveConversationMessagesStartsFromLastCompactionBoundary(t *testing.T) {
	sess := session.New("test-agent")
	sess.Messages = []session.Message{
		{ID: "msg-1", Role: "user", Content: "old request"},
		{ID: "msg-2", Role: "assistant", Content: "old summary", Metadata: map[string]interface{}{messageMetadataCompaction: true}},
		{ID: "msg-3", Role: "assistant", Content: "intermediate reply"},
		{ID: "msg-4", Role: "assistant", Content: "latest summary", Metadata: map[string]interface{}{messageMetadataCompaction: "true"}},
		{
			ID:      "msg-5",
			Role:    "assistant",
			Content: "continuing work",
			ToolCalls: []session.ToolCall{
				{ID: "tc-valid", Name: "read", Input: []byte(`{"path":"keep.go"}`)},
				{ID: "tc-invalid", Name: "read", Input: []byte(`[]`)},
			},
		},
		{
			ID:   "msg-6",
			Role: "tool",
			ToolResults: []session.ToolResult{
				{ToolCallID: "tc-valid", Name: "read", Content: "kept"},
				{ToolCallID: "tc-orphan", Name: "read", Content: "dropped"},
			},
		},
	}

	got := (&Agent{}).getActiveConversationMessages(sess)
	if len(got) != 3 {
		t.Fatalf("expected 3 active messages after last compaction boundary, got %d", len(got))
	}
	if got[0].Content != "latest summary" {
		t.Fatalf("expected latest compaction summary to be first active message, got %q", got[0].Content)
	}
	if len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].ID != "tc-valid" {
		t.Fatalf("expected sanitization to keep only the valid tool call, got %#v", got[1].ToolCalls)
	}
	if len(got[2].ToolResults) != 1 || got[2].ToolResults[0].ToolCallID != "tc-valid" {
		t.Fatalf("expected sanitization to keep only the matching tool result, got %#v", got[2].ToolResults)
	}
}

func TestBuildCompactionRequestFromMessagesAggregatesAndTruncates(t *testing.T) {
	a := &Agent{config: Config{Model: "test-model"}}
	longResult := strings.Repeat("x", 510)
	messages := []session.Message{
		{Role: "user", Content: "Investigate the failure"},
		{
			Role:      "assistant",
			Content:   "I am checking the logs",
			ToolCalls: []session.ToolCall{{Name: "read"}},
		},
		{
			Role: "tool",
			ToolResults: []session.ToolResult{
				{ToolCallID: "tc-1", Name: "read", Content: longResult},
				{ToolCallID: "tc-2", Name: "read", Content: "boom", IsError: true},
			},
		},
	}

	request := a.buildCompactionRequestFromMessages(messages, "compact the context")
	if request.Model != "test-model" {
		t.Fatalf("expected model to be preserved, got %q", request.Model)
	}
	if request.SystemPrompt != "compact the context" {
		t.Fatalf("expected custom compaction prompt, got %q", request.SystemPrompt)
	}
	if len(request.Messages) != 1 {
		t.Fatalf("expected a single aggregated message, got %d", len(request.Messages))
	}
	if request.Messages[0].Role != "user" {
		t.Fatalf("expected aggregated message role to be user, got %q", request.Messages[0].Role)
	}

	content := request.Messages[0].Content
	if !strings.Contains(content, "USER:\nInvestigate the failure") {
		t.Fatalf("expected user content in aggregated compaction request, got %q", content)
	}
	if !strings.Contains(content, "ASSISTANT:\nI am checking the logs") {
		t.Fatalf("expected assistant content in aggregated compaction request, got %q", content)
	}
	if !strings.Contains(content, "[Called tool: read]") {
		t.Fatalf("expected tool call marker in aggregated compaction request, got %q", content)
	}
	if !strings.Contains(content, "[Tool result: "+truncateForCompaction(longResult, 500)+"]") {
		t.Fatalf("expected truncated tool result in aggregated compaction request, got %q", content)
	}
	if !strings.Contains(content, "[Tool error: boom]") {
		t.Fatalf("expected tool error marker in aggregated compaction request, got %q", content)
	}
}
