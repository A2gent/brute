package agent

import (
	"encoding/json"
	"strings"

	"github.com/A2gent/brute/internal/session"
)

func (a *Agent) getActiveConversationMessages(sess *session.Session) []session.Message {
	if sess == nil || len(sess.Messages) == 0 {
		return nil
	}

	start := 0
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if isCompactionMessage(sess.Messages[i]) {
			start = i
			break
		}
	}
	return sanitizeConversationForLLM(sess.Messages[start:])
}

func sanitizeConversationForLLM(messages []session.Message) []session.Message {
	if len(messages) == 0 {
		return nil
	}

	knownToolCalls := make(map[string]struct{})
	out := make([]session.Message, 0, len(messages))

	for _, msg := range messages {
		switch msg.Role {
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				validCalls := make([]session.ToolCall, 0, len(msg.ToolCalls))
				for _, tc := range msg.ToolCalls {
					if tc.ID == "" || tc.Name == "" {
						continue
					}
					trimmed := strings.TrimSpace(string(tc.Input))
					// Gemini expects JSON object arguments for function calls.
					if trimmed == "" || !json.Valid(tc.Input) || !strings.HasPrefix(trimmed, "{") {
						continue
					}
					validCalls = append(validCalls, tc)
					knownToolCalls[tc.ID] = struct{}{}
				}
				msg.ToolCalls = validCalls
			}
			out = append(out, msg)
		case "tool":
			if len(msg.ToolResults) == 0 {
				out = append(out, msg)
				continue
			}
			filtered := make([]session.ToolResult, 0, len(msg.ToolResults))
			for _, tr := range msg.ToolResults {
				if _, ok := knownToolCalls[tr.ToolCallID]; ok {
					filtered = append(filtered, tr)
				}
			}
			if len(filtered) == 0 {
				continue
			}
			msg.ToolResults = filtered
			out = append(out, msg)
		default:
			out = append(out, msg)
		}
	}

	return out
}

func isCompactionMessage(msg session.Message) bool {
	if msg.Metadata == nil {
		return false
	}
	raw, ok := msg.Metadata[messageMetadataCompaction]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}
