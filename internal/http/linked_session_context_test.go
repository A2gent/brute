package http

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestBuildLinkedContinuationContextIncludesCompactParentState(t *testing.T) {
	parent := session.New("build")
	parent.SetTitle("Fix uploads")
	parent.AddUserMessage("original request: fix upload previews")
	sourceID := parent.Messages[0].ID
	parent.TaskProgress = "- [x] inspect layout\n- [ ] finish provider handoff"
	parent.AddAssistantMessage("old implementation notes", nil)
	parent.AddAssistantMessage("latest compact summary", nil)
	parent.Messages[len(parent.Messages)-1].Metadata = map[string]interface{}{
		"context_compaction": true,
	}
	parent.AddUserMessage("latest user nudge")
	parent.AddToolResult([]session.ToolResult{{
		ToolCallID: "call-1",
		Name:       "bash",
		Content:    "git status output",
	}})

	ctx := buildLinkedContinuationContext(parent, "continue with another provider")

	if !strings.HasPrefix(ctx.Prompt, "continue with another provider") {
		t.Fatalf("expected request at prompt start, got %q", ctx.Prompt[:min(len(ctx.Prompt), 80)])
	}
	for _, want := range []string{
		"Parent session context (compact; full transcript omitted intentionally)",
		"Parent title: Fix uploads",
		"Original message id: " + sourceID,
		"original request: fix upload previews",
		"- [ ] finish provider handoff",
		"latest compact summary",
		"latest user nudge",
		"Tool result bash (ok",
	} {
		if !strings.Contains(ctx.Prompt, want) {
			t.Fatalf("expected prompt to contain %q, got:\n%s", want, ctx.Prompt)
		}
	}
	if ctx.SourceMessageID != sourceID {
		t.Fatalf("source message id = %q, want %q", ctx.SourceMessageID, sourceID)
	}
	if ctx.ParentMessageCount != len(parent.Messages) {
		t.Fatalf("parent message count = %d, want %d", ctx.ParentMessageCount, len(parent.Messages))
	}
}

func TestBuildLinkedContinuationContextCapsHugeParentTranscript(t *testing.T) {
	parent := session.New("build")
	parent.AddUserMessage(strings.Repeat("initial ", 1000) + "ORIGINAL_TAIL_SHOULD_NOT_APPEAR")
	for i := 0; i < 20; i++ {
		parent.AddAssistantMessage(strings.Repeat("assistant-output ", 1000)+"ASSISTANT_TAIL_SHOULD_NOT_APPEAR", nil)
	}
	parent.TaskProgress = strings.Repeat("progress ", 1000) + "PROGRESS_TAIL_SHOULD_NOT_APPEAR"

	ctx := buildLinkedContinuationContext(parent, "continue")

	if !ctx.Truncated {
		t.Fatalf("expected context to be marked truncated")
	}
	if len([]rune(ctx.Prompt)) > linkedContinuationPromptLimit+120 {
		t.Fatalf("prompt length = %d, expected compact cap", len([]rune(ctx.Prompt)))
	}
	for _, forbidden := range []string{
		"ORIGINAL_TAIL_SHOULD_NOT_APPEAR",
		"ASSISTANT_TAIL_SHOULD_NOT_APPEAR",
		"PROGRESS_TAIL_SHOULD_NOT_APPEAR",
	} {
		if strings.Contains(ctx.Prompt, forbidden) {
			t.Fatalf("prompt leaked oversized tail %q", forbidden)
		}
	}
}

func TestLinkedContinuationMetadataMarksCompactContext(t *testing.T) {
	ctx := linkedContinuationContext{
		Prompt:             "continue",
		ParentSessionID:    "parent-1",
		SourceMessageID:    "message-1",
		ParentMessageCount: 12,
		RecentMessageCount: 4,
		Truncated:          true,
	}

	sessionMetadata := linkedContinuationSessionMetadata(ctx)
	if sessionMetadata["linked_context_mode"] != "compact" {
		t.Fatalf("session metadata mode = %#v", sessionMetadata["linked_context_mode"])
	}
	if sessionMetadata["linked_source_session_id"] != "parent-1" {
		t.Fatalf("session source id = %#v", sessionMetadata["linked_source_session_id"])
	}
	if sessionMetadata["linked_source_message_id"] != "message-1" {
		t.Fatalf("source message id = %#v", sessionMetadata["linked_source_message_id"])
	}
	if sessionMetadata["linked_context_prompt_limited"] != true {
		t.Fatalf("expected limited flag")
	}

	messageMetadata := linkedContinuationMessageMetadata(ctx)
	if messageMetadata["linked_context"] != true || messageMetadata["context_mode"] != "compact" {
		t.Fatalf("unexpected message metadata: %#v", messageMetadata)
	}
	if messageMetadata["source_session_id"] != "parent-1" || messageMetadata["source_message_id"] != "message-1" {
		t.Fatalf("unexpected source metadata: %#v", messageMetadata)
	}
	if messageMetadata["context_truncated"] != true {
		t.Fatalf("expected message truncated flag")
	}
}
