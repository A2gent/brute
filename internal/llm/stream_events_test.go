package llm

import "testing"

func TestIsResponseProgressEvent(t *testing.T) {
	progress := []StreamEventType{
		StreamEventContentDelta,
		StreamEventReasoningDelta,
		StreamEventToolCallDelta,
		StreamEventToolStarted,
		StreamEventToolUpdated,
		StreamEventToolInputCompleted,
		StreamEventToolCompleted,
		StreamEventToolOutput,
		StreamEventCost,
		StreamEventRuntimeWarning,
	}
	for _, typ := range progress {
		if !IsResponseProgressEvent(StreamEvent{Type: typ}) {
			t.Fatalf("expected %q to be progress", typ)
		}
	}

	nonProgress := []StreamEventType{
		StreamEventUsage,
		StreamEventProviderTrace,
	}
	for _, typ := range nonProgress {
		if IsResponseProgressEvent(StreamEvent{Type: typ}) {
			t.Fatalf("expected %q to not be progress", typ)
		}
	}
}
