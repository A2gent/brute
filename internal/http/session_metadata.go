// session_metadata.go keeps shared session metadata helpers together after splitting server.go.
package http

import (
	"fmt"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"strings"
	"time"
)

const (
	sessionLinkTypeReview       = "review"
	sessionLinkTypeContinuation = "continuation"
)

func (s *Server) applyProviderTraceToSession(sess *session.Session, targetProvider config.ProviderType, trace *agent.ProviderTraceEvent) {
	if sess == nil || trace == nil {
		return
	}
	changed := false

	if shouldPersistProviderFailure(trace.Phase) {
		appendSessionProviderFailure(sess, trace)
		changed = true
	}

	if config.IsFallbackAggregateRef(string(targetProvider)) || targetProvider == config.ProviderFallback {
		switch strings.TrimSpace(trace.Phase) {
		case "completed":
			if trace.NodeIndex > 0 {
				setSessionFallbackStartIndex(sess, targetProvider, trace.NodeIndex-1)
				changed = true
			}
		case "switching_provider":
			if trace.NodeIndex > 0 {

				setSessionFallbackStartIndex(sess, targetProvider, trace.NodeIndex)
				changed = true
			}
		}
	}

	if changed {
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist provider trace metadata: %v", err)
		}
	}
}

func sessionProviderAndModel(sess *session.Session) (string, string) {
	if sess == nil || sess.Metadata == nil {
		return "", ""
	}

	provider := ""
	model := ""
	if rawProvider, ok := sess.Metadata["provider"]; ok {
		if v, ok := rawProvider.(string); ok {
			provider = strings.TrimSpace(v)
		}
	}
	if rawModel, ok := sess.Metadata["model"]; ok {
		if v, ok := rawModel.(string); ok {
			model = strings.TrimSpace(v)
		}
	}

	return provider, model
}

func normalizeSessionLinkType(raw string) (string, error) {
	normalized := strings.TrimSpace(strings.ToLower(raw))
	if normalized == "" {
		return "", nil
	}
	switch normalized {
	case sessionLinkTypeReview, sessionLinkTypeContinuation:
		return normalized, nil
	default:
		return "", fmt.Errorf("invalid link_type: %s", raw)
	}
}

func sessionLinkType(sess *session.Session) string {
	if sess == nil || sess.Metadata == nil {
		return ""
	}
	value, ok := sess.Metadata["link_type"].(string)
	if !ok {
		return ""
	}
	normalized, err := normalizeSessionLinkType(value)
	if err != nil {
		return ""
	}
	return normalized
}

func sessionRoutedProviderAndModel(sess *session.Session) (string, string) {
	if sess == nil || sess.Metadata == nil {
		return "", ""
	}

	provider := ""
	model := ""
	if rawProvider, ok := sess.Metadata["routed_provider"]; ok {
		if v, ok := rawProvider.(string); ok {
			provider = strings.TrimSpace(v)
		}
	}
	if rawModel, ok := sess.Metadata["routed_model"]; ok {
		if v, ok := rawModel.(string); ok {
			model = strings.TrimSpace(v)
		}
	}

	return provider, model
}

func setSessionRoutedProviderAndModel(sess *session.Session, requestedProvider config.ProviderType, routedProvider config.ProviderType, routedModel string) bool {
	if sess == nil {
		return false
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}

	changed := false
	if requestedProvider != config.ProviderAutoRouter {
		if _, ok := sess.Metadata["routed_provider"]; ok {
			delete(sess.Metadata, "routed_provider")
			changed = true
		}
		if _, ok := sess.Metadata["routed_model"]; ok {
			delete(sess.Metadata, "routed_model")
			changed = true
		}
		return changed
	}

	nextProvider := strings.TrimSpace(string(routedProvider))
	nextModel := strings.TrimSpace(routedModel)

	currentProvider, _ := sess.Metadata["routed_provider"].(string)
	currentModel, _ := sess.Metadata["routed_model"].(string)

	if strings.TrimSpace(currentProvider) != nextProvider {
		if nextProvider == "" {
			delete(sess.Metadata, "routed_provider")
		} else {
			sess.Metadata["routed_provider"] = nextProvider
		}
		changed = true
	}

	if strings.TrimSpace(currentModel) != nextModel {
		if nextModel == "" {
			delete(sess.Metadata, "routed_model")
		} else {
			sess.Metadata["routed_model"] = nextModel
		}
		changed = true
	}

	return changed
}

func storageSessionProviderAndModel(sess *storage.Session) (string, string) {
	if sess == nil || sess.Metadata == nil {
		return "", ""
	}

	provider := ""
	model := ""
	if rawProvider, ok := sess.Metadata["provider"]; ok {
		if v, ok := rawProvider.(string); ok {
			provider = strings.TrimSpace(v)
		}
	}
	if rawModel, ok := sess.Metadata["model"]; ok {
		if v, ok := rawModel.(string); ok {
			model = strings.TrimSpace(v)
		}
	}

	return provider, model
}

func storageSessionRoutedProviderAndModel(sess *storage.Session) (string, string) {
	if sess == nil || sess.Metadata == nil {
		return "", ""
	}

	provider := ""
	model := ""
	if rawProvider, ok := sess.Metadata["routed_provider"]; ok {
		if v, ok := rawProvider.(string); ok {
			provider = strings.TrimSpace(v)
		}
	}
	if rawModel, ok := sess.Metadata["routed_model"]; ok {
		if v, ok := rawModel.(string); ok {
			model = strings.TrimSpace(v)
		}
	}

	return provider, model
}

func sessionTotalTokens(sess *session.Session) int {
	if sess == nil || sess.Metadata == nil {
		return 0
	}
	input := metadataNumber(sess.Metadata, "total_input_tokens")
	output := metadataNumber(sess.Metadata, "total_output_tokens")
	total := int(input + output)
	if total < 0 {
		return 0
	}
	return total
}

func sessionInputOutputTokens(sess *session.Session) (int, int) {
	if sess == nil || sess.Metadata == nil {
		return 0, 0
	}
	input := int(metadataNumber(sess.Metadata, "total_input_tokens"))
	output := int(metadataNumber(sess.Metadata, "total_output_tokens"))
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	return input, output
}

func storageSessionTotalTokens(sess *storage.Session) int {
	if sess == nil || sess.Metadata == nil {
		return 0
	}
	input := metadataNumber(sess.Metadata, "total_input_tokens")
	output := metadataNumber(sess.Metadata, "total_output_tokens")
	total := int(input + output)
	if total < 0 {
		return 0
	}
	return total
}

func sessionRunDurationSeconds(createdAt time.Time, updatedAt time.Time, status string) int64 {
	end := updatedAt
	if strings.EqualFold(strings.TrimSpace(status), string(session.StatusRunning)) {
		end = time.Now()
	}
	if end.Before(createdAt) {
		return 0
	}
	return int64(end.Sub(createdAt).Seconds())
}

const providerFailuresMetadataKey = "provider_failures"

const fallbackProgressMetadataKey = "fallback_progress"

func shouldPersistProviderFailure(phase string) bool {
	switch strings.TrimSpace(phase) {
	case "attempt_failed", "attempt_failed_partial", "retry_layer_failed", "switching_provider":
		return true
	default:
		return false
	}
}

func appendSessionProviderFailure(sess *session.Session, trace *agent.ProviderTraceEvent) {
	if sess == nil || trace == nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	existing := sessionProviderFailures(sess.Metadata)
	entry := ProviderFailurePayload{
		Timestamp:     time.Now(),
		Provider:      strings.TrimSpace(trace.Provider),
		Model:         strings.TrimSpace(trace.Model),
		Attempt:       trace.Attempt,
		MaxAttempts:   trace.MaxAttempts,
		NodeIndex:     trace.NodeIndex,
		TotalNodes:    trace.TotalNodes,
		Phase:         strings.TrimSpace(trace.Phase),
		Reason:        strings.TrimSpace(trace.Reason),
		FallbackTo:    strings.TrimSpace(trace.FallbackTo),
		FallbackModel: strings.TrimSpace(trace.FallbackModel),
	}
	existing = append(existing, entry)
	if len(existing) > 100 {
		existing = existing[len(existing)-100:]
	}
	serialized := make([]map[string]interface{}, 0, len(existing))
	for _, item := range existing {
		serialized = append(serialized, map[string]interface{}{
			"timestamp":      item.Timestamp.Format(time.RFC3339Nano),
			"provider":       item.Provider,
			"model":          item.Model,
			"attempt":        item.Attempt,
			"max_attempts":   item.MaxAttempts,
			"node_index":     item.NodeIndex,
			"total_nodes":    item.TotalNodes,
			"phase":          item.Phase,
			"reason":         item.Reason,
			"fallback_to":    item.FallbackTo,
			"fallback_model": item.FallbackModel,
		})
	}
	sess.Metadata[providerFailuresMetadataKey] = serialized
}

func sessionFallbackStartIndex(sess *session.Session, providerRef config.ProviderType) int {
	if sess == nil || sess.Metadata == nil {
		return 0
	}
	raw, ok := sess.Metadata[fallbackProgressMetadataKey]
	if !ok || raw == nil {
		return 0
	}
	byProvider, ok := raw.(map[string]interface{})
	if !ok {
		return 0
	}
	key := config.NormalizeProviderRef(string(providerRef))
	if key == "" {
		return 0
	}
	return int(metadataNumber(byProvider, key))
}

func setSessionFallbackStartIndex(sess *session.Session, providerRef config.ProviderType, idx int) {
	if sess == nil {
		return
	}
	if idx < 0 {
		idx = 0
	}
	key := config.NormalizeProviderRef(string(providerRef))
	if key == "" {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	raw, _ := sess.Metadata[fallbackProgressMetadataKey].(map[string]interface{})
	if raw == nil {
		raw = map[string]interface{}{}
	}
	raw[key] = idx
	sess.Metadata[fallbackProgressMetadataKey] = raw
}

func sessionProviderFailures(metadata map[string]interface{}) []ProviderFailurePayload {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata[providerFailuresMetadataKey]
	if !ok || raw == nil {
		return nil
	}
	rows, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]ProviderFailurePayload, 0, len(rows))
	for _, row := range rows {
		rowMap, ok := row.(map[string]interface{})
		if !ok {
			continue
		}
		entry := ProviderFailurePayload{}
		if v, ok := rowMap["timestamp"].(string); ok && strings.TrimSpace(v) != "" {
			if ts, err := time.Parse(time.RFC3339Nano, v); err == nil {
				entry.Timestamp = ts
			}
		}
		if v, ok := rowMap["provider"].(string); ok {
			entry.Provider = strings.TrimSpace(v)
		}
		if v, ok := rowMap["model"].(string); ok {
			entry.Model = strings.TrimSpace(v)
		}
		entry.Attempt = int(metadataNumber(rowMap, "attempt"))
		entry.MaxAttempts = int(metadataNumber(rowMap, "max_attempts"))
		entry.NodeIndex = int(metadataNumber(rowMap, "node_index"))
		entry.TotalNodes = int(metadataNumber(rowMap, "total_nodes"))
		if v, ok := rowMap["phase"].(string); ok {
			entry.Phase = strings.TrimSpace(v)
		}
		if v, ok := rowMap["reason"].(string); ok {
			entry.Reason = strings.TrimSpace(v)
		}
		if v, ok := rowMap["fallback_to"].(string); ok {
			entry.FallbackTo = strings.TrimSpace(v)
		}
		if v, ok := rowMap["fallback_model"].(string); ok {
			entry.FallbackModel = strings.TrimSpace(v)
		}
		out = append(out, entry)
	}
	return out
}

func metadataNumber(metadata map[string]interface{}, key string) float64 {
	if metadata == nil {
		return 0
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return 0
	}

	switch v := raw.(type) {
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
	case uint:
		return float64(v)
	case uint64:
		return float64(v)
	case uint32:
		return float64(v)
	default:
		return 0
	}
}
