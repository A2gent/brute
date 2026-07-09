package http

import (
	"encoding/json"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
)

func (s *Server) agentEventToStreamEvent(sess *session.Session, providerType config.ProviderType, ev agent.Event) (ChatStreamEvent, bool) {
	switch ev.Type {
	case agent.EventAssistantDelta:
		return ChatStreamEvent{
			Type:  "assistant_delta",
			Delta: ev.Delta,
		}, true
	case agent.EventToolExecuting:
		toolCalls := make([]StreamToolCallEvent, len(ev.ToolCalls))
		for i, tc := range ev.ToolCalls {
			toolCalls[i] = StreamToolCallEvent{
				ID:               tc.ID,
				Name:             tc.Name,
				Input:            json.RawMessage(tc.Input),
				ThoughtSignature: tc.ThoughtSignature,
			}
		}
		return ChatStreamEvent{
			Type:      "tool_executing",
			Step:      ev.Step,
			Message:   streamLastMessageResponse(s, sess),
			ToolCalls: toolCalls,
		}, true
	case agent.EventToolProgress:
		if ev.ToolProgress == nil {
			return ChatStreamEvent{}, false
		}
		return ChatStreamEvent{
			Type: "tool_progress",
			Step: ev.Step,
			ToolProgress: &StreamToolProgressEvent{
				ToolCallID: ev.ToolProgress.ToolCallID,
				ToolName:   ev.ToolProgress.ToolName,
				Status:     ev.ToolProgress.Status,
				Content:    ev.ToolProgress.Content,
				Metadata:   ev.ToolProgress.Metadata,
			},
		}, true
	case agent.EventToolCompleted:
		event := ChatStreamEvent{
			Type:   "tool_completed",
			Step:   ev.Step,
			Status: string(sess.Status),
		}
		if len(sess.Messages) > 0 {
			msg := s.messageToResponse(sess.Messages[len(sess.Messages)-1])
			event.Message = &msg
		}
		return event, true
	case agent.EventStepCompleted:
		return ChatStreamEvent{
			Type: "step_completed",
			Step: ev.Step,
		}, true
	case agent.EventProviderTrace:
		if ev.Provider == nil {
			return ChatStreamEvent{}, false
		}
		// Provider traces mutate session metadata and must happen in every run mode so
		// background streams and direct POST streams stay consistent.
		s.applyProviderTraceToSession(sess, providerType, ev.Provider)
		return ChatStreamEvent{
			Type: "provider_trace",
			Step: ev.Step,
			Provider: &StreamProviderEvent{
				Provider:      ev.Provider.Provider,
				Model:         ev.Provider.Model,
				Attempt:       ev.Provider.Attempt,
				MaxAttempts:   ev.Provider.MaxAttempts,
				NodeIndex:     ev.Provider.NodeIndex,
				TotalNodes:    ev.Provider.TotalNodes,
				Phase:         ev.Provider.Phase,
				Reason:        ev.Provider.Reason,
				FallbackTo:    ev.Provider.FallbackTo,
				FallbackModel: ev.Provider.FallbackModel,
				Recovered:     ev.Provider.Recovered,
			},
		}, true
	default:
		return ChatStreamEvent{}, false
	}
}
