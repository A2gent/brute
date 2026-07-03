package tui

import (
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agent"
	httpserver "github.com/A2gent/brute/internal/http"
	"github.com/A2gent/brute/internal/session"
)

func TestUpdateAgentEventStreamsAssistantDeltasIntoLiveMessage(t *testing.T) {
	m := Model{}

	m = m.updateAgentEvent(agent.Event{Type: agent.EventAssistantDelta, Delta: "Hel"})
	m = m.updateAgentEvent(agent.Event{Type: agent.EventAssistantDelta, Delta: "lo"})

	if len(m.messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(m.messages))
	}
	if got, want := m.messages[0].role, "assistant"; got != want {
		t.Fatalf("message role = %q, want %q", got, want)
	}
	if got, want := m.messages[0].content, "Hello"; got != want {
		t.Fatalf("streamed content = %q, want %q", got, want)
	}
	if !messageMetadataBool(m.messages[0].metadata, tuiLiveStreamMetadataKey) {
		t.Fatal("expected streamed assistant message to carry live metadata")
	}
}

func TestUpdateAgentEventAttachesToolCallsToLiveAssistant(t *testing.T) {
	m := Model{}

	m = m.updateAgentEvent(agent.Event{Type: agent.EventAssistantDelta, Delta: "Checking."})
	m = m.updateAgentEvent(agent.Event{
		Type: agent.EventToolExecuting,
		Step: 1,
		ToolCalls: []agent.ToolCallEvent{{
			ID:    "call-1",
			Name:  "bash",
			Input: `{"command":"echo ok"}`,
		}},
	})

	if len(m.messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(m.messages))
	}
	if len(m.messages[0].toolCalls) != 1 {
		t.Fatalf("toolCalls len = %d, want 1", len(m.messages[0].toolCalls))
	}
	if got, want := m.messages[0].toolCalls[0].Name, "bash"; got != want {
		t.Fatalf("tool name = %q, want %q", got, want)
	}
	if got, want := m.activeRunStatus, "Running step 1 tool"; got != want {
		t.Fatalf("activeRunStatus = %q, want %q", got, want)
	}
}

func TestApplySyncedSessionPreservesLiveAssistantBeforeInjectedMessage(t *testing.T) {
	sess := session.New("build")
	sess.AddUserMessage("initial task")

	m := Model{
		session:                    sess,
		messages:                   messagesFromSession(sess),
		lastSyncedMessageCount:     len(sess.Messages),
		lastSyncedSessionUpdatedAt: sess.UpdatedAt,
	}
	m.appendAssistantDelta("partial response")

	fresh := *sess
	fresh.Messages = append([]session.Message(nil), sess.Messages...)
	fresh.AddUserMessageWithImagesAndMetadata("note from Caesar", nil, map[string]interface{}{
		"injected_during_run": true,
	})

	m.applySyncedSession(&fresh)

	if len(m.messages) != 3 {
		t.Fatalf("messages len = %d, want 3: %#v", len(m.messages), m.messages)
	}
	if got, want := m.messages[0].role, "user"; got != want {
		t.Fatalf("message[0] role = %q, want %q", got, want)
	}
	if got, want := m.messages[1].role, "assistant"; got != want {
		t.Fatalf("message[1] role = %q, want %q", got, want)
	}
	if got, want := m.messages[1].content, "partial response"; got != want {
		t.Fatalf("message[1] content = %q, want %q", got, want)
	}
	if got, want := m.messages[2].content, "note from Caesar"; got != want {
		t.Fatalf("message[2] content = %q, want %q", got, want)
	}
}

func TestUpdateExternalSessionEventStreamsCaesarDeltas(t *testing.T) {
	m := Model{}

	m = m.updateExternalSessionEvent(httpserver.ChatStreamEvent{Type: "assistant_delta", Delta: "from "})
	m = m.updateExternalSessionEvent(httpserver.ChatStreamEvent{Type: "assistant_delta", Delta: "Caesar"})

	if len(m.messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(m.messages))
	}
	if got, want := m.messages[0].content, "from Caesar"; got != want {
		t.Fatalf("streamed content = %q, want %q", got, want)
	}
}

func TestUpdateExternalSessionEventDoneReplacesLiveTranscript(t *testing.T) {
	m := Model{}
	m = m.updateExternalSessionEvent(httpserver.ChatStreamEvent{Type: "assistant_delta", Delta: "partial"})

	now := time.Now()
	m = m.updateExternalSessionEvent(httpserver.ChatStreamEvent{
		Type:   "done",
		Status: string(session.StatusCompleted),
		Messages: []httpserver.MessageResponse{{
			ID:        "assistant-1",
			Role:      "assistant",
			Content:   "final",
			Timestamp: now,
		}},
	})

	if len(m.messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(m.messages))
	}
	if got, want := m.messages[0].content, "final"; got != want {
		t.Fatalf("message content = %q, want %q", got, want)
	}
	if messageMetadataBool(m.messages[0].metadata, tuiLiveStreamMetadataKey) {
		t.Fatal("final transcript should not retain live stream metadata")
	}
}
