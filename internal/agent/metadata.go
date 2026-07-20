package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
)

func llmTimingMetadata(startedAt, completedAt time.Time, provider, model string, extra ...map[string]interface{}) map[string]interface{} {
	if completedAt.Before(startedAt) {
		completedAt = startedAt
	}
	metadata := map[string]interface{}{
		"llm_duration_ms":  completedAt.Sub(startedAt).Milliseconds(),
		"llm_started_at":   startedAt.UTC().Format(time.RFC3339Nano),
		"llm_completed_at": completedAt.UTC().Format(time.RFC3339Nano),
	}
	if trimmedProvider := strings.TrimSpace(provider); trimmedProvider != "" {
		metadata["llm_provider"] = trimmedProvider
	}
	if trimmedModel := strings.TrimSpace(model); trimmedModel != "" {
		metadata["llm_model"] = trimmedModel
	}
	for _, values := range extra {
		for key, value := range values {
			metadata[key] = value
		}
	}
	return metadata
}

func (a *Agent) addTokenUsageMetadata(sess *session.Session, usage llm.TokenUsage) {
	if sess == nil {
		return
	}
	if a.config.ContextWindow > 0 {
		metadataSetFloat(sess, metadataContextWindow, float64(a.config.ContextWindow))
	}

	// Get previous context size before updating
	prevContext := metadataFloat(sess.Metadata, metadataCurrentContextTokens)

	// OutputTokens are new tokens generated in this step - accumulate them
	metadataSetFloat(sess, metadataTotalOutputTokens, metadataFloat(sess.Metadata, metadataTotalOutputTokens)+float64(usage.OutputTokens))
	if usage.CachedInputTokens > 0 {
		metadataSetFloat(sess, metadataTotalCachedTokens, metadataFloat(sess.Metadata, metadataTotalCachedTokens)+float64(usage.CachedInputTokens))
	}
	if usage.ReasoningTokens > 0 {
		metadataSetFloat(sess, metadataTotalReasoningTokens, metadataFloat(sess.Metadata, metadataTotalReasoningTokens)+float64(usage.ReasoningTokens))
	}

	// InputTokens from API represent the FULL context size (system prompt + all history)
	// NOT incremental tokens, so we should NOT accumulate them.
	// Store the current full context size.
	metadataSetFloat(sess, metadataCurrentContextTokens, float64(usage.InputTokens))

	// For total_input_tokens, we track cumulative input by calculating the delta:
	// new tokens = current input - previous context size
	if prevContext == 0 {
		// First call - use InputTokens as-is (includes system prompt)
		metadataSetFloat(sess, metadataTotalInputTokens, float64(usage.InputTokens))
	} else {
		// Subsequent calls - add only the delta (new user message + new output from previous step)
		deltaTokens := float64(usage.InputTokens) - prevContext
		if deltaTokens > 0 {
			metadataSetFloat(sess, metadataTotalInputTokens, metadataFloat(sess.Metadata, metadataTotalInputTokens)+deltaTokens)
		}
	}
}

func metadataFloat(metadata map[string]interface{}, key string) float64 {
	if metadata == nil {
		return 0
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return 0
	}
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case int32:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f
	default:
		return 0
	}
}

func metadataSetFloat(sess *session.Session, key string, value float64) {
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	sess.Metadata[key] = value
}

func metadataSetString(sess *session.Session, key string, value string) {
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	sess.Metadata[key] = value
}

func lastResponseIDForStatefulRequest(sess *session.Session) string {
	if sess == nil || sess.Metadata == nil {
		return ""
	}
	raw, _ := sess.Metadata[metadataLastResponseID].(string)
	return strings.TrimSpace(raw)
}

func lastProviderSessionIdentityForRequest(sess *session.Session) string {
	if sess == nil || sess.Metadata == nil {
		return ""
	}
	raw, _ := sess.Metadata[metadataProviderSessionIdentity].(string)
	return strings.TrimSpace(raw)
}

func lastProviderSessionCursorForRequest(sess *session.Session) string {
	if sess == nil || sess.Metadata == nil {
		return ""
	}
	raw, _ := sess.Metadata[metadataProviderSessionCursor].(string)
	return strings.TrimSpace(raw)
}

func messagesAfterProviderSessionCursor(messages []session.Message, cursor string) []session.Message {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return messages
	}
	for i := len(messages) - 1; i >= 0; i-- {
		raw, _ := messages[i].Metadata[messageMetadataProviderSessionCursor].(string)
		if strings.TrimSpace(raw) == cursor {
			if i+1 >= len(messages) {
				return nil
			}
			return messages[i+1:]
		}
	}
	return messages
}

func messagesAfterResponseID(messages []session.Message, responseID string) []session.Message {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return messages
	}
	for i := len(messages) - 1; i >= 0; i-- {
		raw, _ := messages[i].Metadata[messageMetadataResponseID].(string)
		if strings.TrimSpace(raw) == responseID {
			if i+1 >= len(messages) {
				return nil
			}
			return messages[i+1:]
		}
	}
	return messages
}
