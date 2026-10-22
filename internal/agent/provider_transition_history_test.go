package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
)

func TestTransitionSummaryOmitsBinaryAndRuntimeState(t *testing.T) {
	payload := strings.Repeat("BINARY_PAYLOAD", 1400000)
	messages := []session.Message{
		{Role: "user", Content: "Inspect the tower", Images: []session.ImageAttachment{{Name: "tower.png", MediaType: "image/png", DataBase64: payload, Path: "images/tower.png"}, {Name: "reference.png", URL: "data:image/png;base64,INLINE_PIXELS"}}, Metadata: map[string]interface{}{"runtime": payload}},
		{Role: "assistant", ToolCalls: []session.ToolCall{{ID: "call", Name: "read", Input: json.RawMessage(`{"path":"tower.glb"}`), ThoughtSignature: payload}}},
		{Role: "tool", ToolResults: []session.ToolResult{{ToolCallID: "call", Name: "read", Content: "model saved to tower.glb", IsError: true, Metadata: map[string]interface{}{"admission_images": payload}}}},
	}
	sess := session.New("test")
	sess.Messages = messages
	original, _ := json.Marshal(sess.Messages)
	target := &MockLLM{Response: &llm.ChatResponse{Content: "summary"}}
	var stats session.TransitionCompaction
	summary, _, before, err := (&Agent{}).summarizeTransition(context.Background(), sess, target, "target", 65536, &stats)
	if err != nil || summary != "summary" || before != len(original) {
		t.Fatalf("summary=%q before=%d err=%v", summary, before, err)
	}
	if len(target.CapturedRequests) != 1 {
		t.Fatalf("expected one text-only summary request, got %d", len(target.CapturedRequests))
	}
	text := target.CapturedRequests[0].Messages[0].Content
	for _, want := range []string{"Inspect the tower", "tower.png", "images/tower.png", "tower.glb", "model saved", `"is_error":true`, `"tool_call_id":"call"`} {
		if !strings.Contains(text, want) {
			t.Errorf("summary input missing %q", want)
		}
	}
	if strings.Contains(text, "BINARY_PAYLOAD") || strings.Contains(text, "INLINE_PIXELS") || strings.Contains(text, "thought_signature") || strings.Contains(text, "metadata") {
		t.Fatal("binary or runtime state leaked into text summary")
	}
	after, _ := json.Marshal(sess.Messages)
	if !reflect.DeepEqual(original, after) {
		t.Fatal("archived messages changed")
	}
}

func TestTransitionSummaryLargeTextUsesChunks(t *testing.T) {
	sess := session.New("test")
	sess.AddUserMessage("original task")
	sess.AddAssistantMessage(strings.Repeat("historical tower details\n", 750000), nil)
	sess.AddUserMessage("pending prompt must stay out of summary")
	target := &MockLLM{Response: &llm.ChatResponse{Content: "summary", Usage: llm.TokenUsage{InputTokens: 1}}}
	var stats session.TransitionCompaction
	_, usage, before, err := (&Agent{}).summarizeTransition(context.Background(), sess, target, "target", 1000000, &stats)
	if err != nil || before <= 16*1024*1024 || len(target.CapturedRequests) < 2 {
		t.Fatalf("before=%d calls=%d err=%v", before, len(target.CapturedRequests), err)
	}
	if usage.InputTokens != len(target.CapturedRequests) {
		t.Fatal("chunk usage was not accumulated")
	}
	for _, req := range target.CapturedRequests {
		if strings.Contains(req.Messages[0].Content, "pending prompt must stay out") || transitionRequestEstimate(req)+req.MaxTokens > 1000000 {
			t.Fatal("pending prompt leaked or chunk exceeded target window")
		}
	}
}

func TestTransitionSummaryRequestBudget(t *testing.T) {
	sess := session.New("test")
	sess.AddAssistantMessage(strings.Repeat("large history ", 100000), nil)
	target := &MockLLM{Response: &llm.ChatResponse{Content: "summary"}}
	stats := session.TransitionCompaction{JevUsed: true, JevDropped: 7}
	_, _, _, err := (&Agent{}).summarizeTransition(context.Background(), sess, target, "target", 4096, &stats)
	if err == nil || !strings.Contains(err.Error(), "summary requests") || len(target.CapturedRequests) != 0 {
		t.Fatalf("expected actionable bounded failure without target egress: %v", err)
	}
	if stats.JevUsed || stats.JevDropped != 0 {
		t.Fatal("stale relevance stats survived retry")
	}
}

func TestTransitionSummaryChunksRespectEncoding(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("башня 🏰\n", 100),
		strings.Repeat("\"\\\n\x00<>&", 100),
	} {
		var restored strings.Builder
		for remaining := text; len(remaining) > 0; {
			size := transitionSummaryChunkSize(remaining, 256)
			if size <= 0 {
				t.Fatal("chunking made no progress")
			}
			chunk := remaining[:size]
			encoded, _ := json.Marshal(chunk)
			if len(encoded) > 256 || !utf8.ValidString(chunk) {
				t.Fatalf("invalid or oversized encoded chunk: %d bytes", len(encoded))
			}
			restored.WriteString(chunk)
			remaining = remaining[size:]
		}
		if restored.String() != text {
			t.Fatal("chunking lost history")
		}
	}
}

func TestTransitionSummaryChecksEscapingBeforeEgress(t *testing.T) {
	window := 4096
	budget := window - len(defaultCompactionPrompt) - 1536
	sess := session.New("test")
	// Raw history fits 121 chunks, but quoting it again as request content does not.
	sess.AddAssistantMessage(strings.Repeat("\"", budget*40), nil)
	raw, err := transitionSummaryHistory(sess.Messages)
	if err != nil || len(raw) >= budget*121 {
		t.Fatal("fixture must pass a raw-byte request-count check")
	}
	target := &MockLLM{Response: &llm.ChatResponse{Content: "summary"}}
	var stats session.TransitionCompaction
	_, _, _, err = (&Agent{}).summarizeTransition(context.Background(), sess, target, "target", window, &stats)
	if err == nil || len(target.CapturedRequests) != 0 {
		t.Fatalf("encoded history must fail before any paid requests: %v", err)
	}
}
