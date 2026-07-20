package llm

// IsResponseProgressEvent reports whether a stream event represents user-visible
// or execution progress that should block retry/fallback after partial emission.
func IsResponseProgressEvent(ev StreamEvent) bool {
	switch ev.Type {
	case StreamEventContentDelta,
		StreamEventReasoningDelta,
		StreamEventToolCallDelta,
		StreamEventToolStarted,
		StreamEventToolUpdated,
		StreamEventToolInputCompleted,
		StreamEventToolCompleted,
		StreamEventToolOutput,
		StreamEventCost,
		StreamEventRuntimeWarning:
		return true
	default:
		return false
	}
}
