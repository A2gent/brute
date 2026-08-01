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
	sessionQueueModeSerial      = "serial"
	sessionQueueModeMetadataKey = "queue_mode"
	sessionQueueAutoStartKey    = "queue_auto_start"
	sessionQueuePausedKey       = "queue_paused"
	// WHY: Marks investigation/decision sessions so Caesar can pin them after completion
	// as a feedback reminder above ordinary fix-it runs.
	sessionNeedsFeedbackKey = "needs_feedback"
)

func sessionNeedsFeedbackFromMetadata(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	raw, ok := metadata[sessionNeedsFeedbackKey]
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

func sessionNeedsFeedback(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	return sessionNeedsFeedbackFromMetadata(sess.Metadata)
}

func setSessionNeedsFeedback(sess *session.Session, enabled bool) {
	if sess == nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	if enabled {
		sess.Metadata[sessionNeedsFeedbackKey] = true
		return
	}
	delete(sess.Metadata, sessionNeedsFeedbackKey)
}

func normalizeSessionQueueMode(raw string) (string, error) {
	normalized := strings.TrimSpace(strings.ToLower(raw))
	if normalized == "" {
		return "", nil
	}
	switch normalized {
	case sessionQueueModeSerial:
		return normalized, nil
	default:
		return "", fmt.Errorf("invalid queue_mode: %s", raw)
	}
}

func applyLeadingSessionQueueDirective(req *CreateSessionRequest) {
	if req == nil {
		return
	}
	prompt, ok := stripLeadingSessionQueueDirective(req.Task)
	if !ok {
		return
	}
	req.Task = prompt
	req.QueueMode = sessionQueueModeSerial
	req.Queued = true
}

func stripLeadingSessionQueueDirective(raw string) (string, bool) {
	trimmed := strings.TrimLeft(raw, " \t\r\n")
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"/queue", "/q", "--queue", "-q"} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		if len(trimmed) > len(prefix) {
			next := trimmed[len(prefix)]
			if next != ':' && next != ' ' && next != '\t' && next != '\r' && next != '\n' {
				continue
			}
		}
		prompt := strings.TrimLeft(trimmed[len(prefix):], " \t\r\n")
		prompt = strings.TrimPrefix(prompt, ":")
		prompt = strings.TrimLeft(prompt, " \t\r\n")
		if strings.TrimSpace(prompt) == "" {
			return "", false
		}
		if strings.HasPrefix(prefix, "/") && isQueueModeSelectionOnly(prompt) {
			return "", false
		}
		return prompt, true
	}
	return "", false
}

func isQueueModeSelectionOnly(raw string) bool {
	normalized := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(raw))), " ")
	switch normalized {
	case "run now", "run_now", "run-now", "now", "start", "immediate", "serial", "queued", "queue":
		return true
	default:
		return false
	}
}

func sessionQueueMode(sess *session.Session) string {
	if sess == nil || sess.Metadata == nil {
		return ""
	}
	raw, ok := sess.Metadata[sessionQueueModeMetadataKey].(string)
	if !ok {
		return ""
	}
	mode, err := normalizeSessionQueueMode(raw)
	if err != nil {
		return ""
	}
	return mode
}

func sessionQueueAutoStart(sess *session.Session) bool {
	if sess == nil || sess.Metadata == nil {
		return false
	}
	raw, ok := sess.Metadata[sessionQueueAutoStartKey]
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

func sessionIsSerialQueuedAutoRun(sess *session.Session) bool {
	return sessionQueueMode(sess) == sessionQueueModeSerial && sessionQueueAutoStart(sess)
}

func sessionIsQueuePaused(sess *session.Session) bool {
	if sess == nil || sess.Metadata == nil {
		return false
	}
	raw, ok := sess.Metadata[sessionQueuePausedKey]
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

func setSessionQueuePaused(sess *session.Session, paused bool) {
	if sess == nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	if paused {
		sess.Metadata[sessionQueuePausedKey] = true
		return
	}
	delete(sess.Metadata, sessionQueuePausedKey)
}

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
		case "provider_selected":
			if setSessionFallbackActiveNode(sess, trace) {
				changed = true
			}
		case "completed":
			if trace.NodeIndex > 0 {
				setSessionFallbackStartIndex(sess, targetProvider, trace.NodeIndex-1)
				changed = true
			}
			if setSessionFallbackActiveNode(sess, trace) {
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

func sessionReasoningEffort(sess *session.Session) string {
	if sess == nil || sess.Metadata == nil {
		return ""
	}
	effort, _ := sess.Metadata["reasoning_effort"].(string)
	return strings.TrimSpace(effort)
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

func sessionFallbackActiveProviderAndModel(sess *session.Session) (string, string) {
	if sess == nil || sess.Metadata == nil {
		return "", ""
	}

	provider := ""
	model := ""
	if rawProvider, ok := sess.Metadata[fallbackActiveProviderMetadataKey]; ok {
		if v, ok := rawProvider.(string); ok {
			provider = strings.TrimSpace(v)
		}
	}
	if rawModel, ok := sess.Metadata[fallbackActiveModelMetadataKey]; ok {
		if v, ok := rawModel.(string); ok {
			model = strings.TrimSpace(v)
		}
	}

	return provider, model
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

func sessionRoutingRuleAndReason(sess *session.Session) (string, string) {
	if sess == nil || sess.Metadata == nil {
		return "", ""
	}
	rule, _ := sess.Metadata["routed_rule"].(string)
	reason, _ := sess.Metadata["routed_reason"].(string)
	return strings.TrimSpace(rule), strings.TrimSpace(reason)
}

func setSessionRoutedProviderAndModel(sess *session.Session, requestedProvider config.ProviderType, routedProvider config.ProviderType, routedModel string, routedRule string, routedReason string) bool {
	if sess == nil {
		return false
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}

	changed := false
	if requestedProvider != config.ProviderAutoRouter {
		for _, key := range []string{"routed_provider", "routed_model", "routed_rule", "routed_reason"} {
			if _, ok := sess.Metadata[key]; ok {
				delete(sess.Metadata, key)
				changed = true
			}
		}
		return changed
	}

	nextProvider := strings.TrimSpace(string(routedProvider))
	nextModel := strings.TrimSpace(routedModel)
	nextRule := strings.TrimSpace(routedRule)
	nextReason := strings.TrimSpace(routedReason)

	currentProvider, _ := sess.Metadata["routed_provider"].(string)
	currentModel, _ := sess.Metadata["routed_model"].(string)
	currentRule, _ := sess.Metadata["routed_rule"].(string)
	currentReason, _ := sess.Metadata["routed_reason"].(string)

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

	if strings.TrimSpace(currentRule) != nextRule {
		if nextRule == "" {
			delete(sess.Metadata, "routed_rule")
		} else {
			sess.Metadata["routed_rule"] = nextRule
		}
		changed = true
	}

	if strings.TrimSpace(currentReason) != nextReason {
		if nextReason == "" {
			delete(sess.Metadata, "routed_reason")
		} else {
			sess.Metadata["routed_reason"] = nextReason
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

const fallbackActiveProviderMetadataKey = "fallback_active_provider"

const fallbackActiveModelMetadataKey = "fallback_active_model"

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

// setSessionFallbackActiveNode records which fallback chain node last served the session so the
// UI can keep showing it after the request ends. Reports whether metadata changed.
func setSessionFallbackActiveNode(sess *session.Session, trace *agent.ProviderTraceEvent) bool {
	if sess == nil || trace == nil {
		return false
	}
	provider := strings.TrimSpace(trace.Provider)
	if provider == "" {
		return false
	}
	model := strings.TrimSpace(trace.Model)
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	changed := false
	if current, _ := sess.Metadata[fallbackActiveProviderMetadataKey].(string); strings.TrimSpace(current) != provider {
		sess.Metadata[fallbackActiveProviderMetadataKey] = provider
		changed = true
	}
	if current, _ := sess.Metadata[fallbackActiveModelMetadataKey].(string); strings.TrimSpace(current) != model {
		sess.Metadata[fallbackActiveModelMetadataKey] = model
		changed = true
	}
	return changed
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
