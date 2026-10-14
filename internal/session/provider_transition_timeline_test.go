package session

import "testing"

func TestProviderTransitionTimelineAnchorIsImmutable(t *testing.T) {
	s := New("test")
	s.AddUserMessage("task")
	tr, _ := NewProviderTransition("ab", ProviderEndpoint{Provider: "a"}, ProviderEndpoint{Provider: "b"}, "manual", "switch_auto")
	PutProviderTransition(s, tr)
	anchor := s.Messages[0].ID
	if tr.Timeline == nil || tr.Timeline.AfterMessageID != anchor || tr.Timeline.MessageCount != 1 {
		t.Fatalf("missing anchor: %+v", tr)
	}
	s.AddAssistantMessage("b response", nil)
	tr.Phase = "completed"
	PutProviderTransition(s, tr)
	restored := CurrentProviderTransition(s)
	if restored.Timeline.AfterMessageID != anchor || restored.Timeline.MessageCount != 1 {
		t.Fatalf("anchor moved: %+v", restored)
	}
	if len(s.Messages) != 2 {
		t.Fatal("notice entered conversational context")
	}
}
