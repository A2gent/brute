package http

import (
	"encoding/json"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"testing"
)

func TestMessageProvenanceStreamAndSnapshot(t *testing.T) {
	s := &Server{}
	sess := session.New("test")
	p := llm.NewProvenance("anthropic:work", "model-a")
	sess.AddMessage(session.Message{Role: "assistant", Content: "old", Provenance: p, ToolCalls: []session.ToolCall{{ID: "t", Name: "read", Input: json.RawMessage(`{}`)}}})
	sess.Metadata["provider"] = "openai:personal"
	for _, event := range []agent.Event{
		{Type: agent.EventAssistantDelta, TurnID: "turn", Delta: "text", Provenance: p},
		{Type: agent.EventLLMRuntime, TurnID: "turn", Provenance: p, Runtime: &llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "thought"}},
		{Type: agent.EventLLMRuntime, TurnID: "turn", Provenance: p, Runtime: &llm.StreamEvent{Type: llm.StreamEventToolStarted, ToolCallID: "native"}},
	} {
		result, ok := s.agentEventToStreamEvent(sess, config.ProviderOpenAI, event)
		if !ok || result.Provenance == nil || *result.Provenance != *p {
			t.Fatalf("stream lost provenance: %+v", result)
		}
	}
	toolEvent, ok := s.agentEventToStreamEvent(sess, config.ProviderOpenAI, agent.Event{Type: agent.EventToolExecuting})
	if !ok || toolEvent.Message == nil || toolEvent.Message.Provenance == nil || *toolEvent.Message.Provenance != *p {
		t.Fatalf("tool message lost provenance: %+v", toolEvent)
	}
	reloaded := session.FromStorage(sess.ToStorage())
	response := s.messageToResponse(reloaded.Messages[0])
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded MessageResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Provenance == nil || *decoded.Provenance != *p {
		t.Fatalf("snapshot lost provenance: %s", data)
	}
	legacy := s.messageToResponse(session.Message{Role: "assistant", Metadata: map[string]interface{}{"llm_provider": "anthropic"}})
	if legacy.Provenance != nil {
		t.Fatal("fabricated legacy provenance")
	}
}
