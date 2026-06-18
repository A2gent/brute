package agent

import (
	"fmt"
	"math"
	"strings"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

const (
	messageMetadataPendingToolResults = "pending_tool_results"
	toolMetadataParallelStepProgress  = "parallel_step_progress"
)

func recordPendingToolProgress(sess *session.Session, progress tools.ProgressEvent) bool {
	toolCallID := strings.TrimSpace(progress.ToolCallID)
	if sess == nil || toolCallID == "" {
		return false
	}

	messageIndex := -1
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		msg := sess.Messages[i]
		if msg.Role != "assistant" {
			continue
		}
		for _, toolCall := range msg.ToolCalls {
			if toolCall.ID == toolCallID {
				messageIndex = i
				break
			}
		}
		if messageIndex >= 0 {
			break
		}
	}
	if messageIndex < 0 {
		return false
	}

	msg := &sess.Messages[messageIndex]
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]interface{})
	}

	pendingResults := pendingToolResultsFromMetadata(msg.Metadata[messageMetadataPendingToolResults])
	updated := pendingToolResultFromProgress(progress, pendingToolResultForCall(pendingResults, toolCallID))
	replaced := false
	for i := range pendingResults {
		if pendingResults[i].ToolCallID == toolCallID {
			pendingResults[i] = updated
			replaced = true
			break
		}
	}
	if !replaced {
		pendingResults = append(pendingResults, updated)
	}
	msg.Metadata[messageMetadataPendingToolResults] = pendingResults
	return true
}

func clearPendingToolProgressMetadata(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	cleared := false
	for i := range sess.Messages {
		if len(sess.Messages[i].Metadata) == 0 {
			continue
		}
		if _, ok := sess.Messages[i].Metadata[messageMetadataPendingToolResults]; ok {
			delete(sess.Messages[i].Metadata, messageMetadataPendingToolResults)
			cleared = true
		}
	}
	return cleared
}

func pendingToolResultForCall(results []session.ToolResult, toolCallID string) *session.ToolResult {
	for i := range results {
		if results[i].ToolCallID == toolCallID {
			return &results[i]
		}
	}
	return nil
}

func pendingToolResultFromProgress(progress tools.ProgressEvent, previous *session.ToolResult) session.ToolResult {
	metadata := cloneInterfaceMap(progress.Metadata)
	status := strings.TrimSpace(progress.Status)
	if status != "" {
		metadata["progress_status"] = status
	}
	metadata["progress_pending"] = true

	result := session.ToolResult{
		ToolCallID: strings.TrimSpace(progress.ToolCallID),
		Name:       strings.TrimSpace(progress.ToolName),
		Content:    strings.TrimSpace(progress.Content),
		IsError:    toolProgressStatusIsError(status),
		Metadata:   metadata,
	}

	stepKey := metadataStepKey(metadata["parallel_step"])
	if stepKey == "" {
		return result
	}

	stepProgress := parallelStepProgressFromMetadata(nil)
	if previous != nil {
		stepProgress = parallelStepProgressFromMetadata(previous.Metadata[toolMetadataParallelStepProgress])
	}
	stepProgress[stepKey] = map[string]interface{}{
		"tool_call_id": result.ToolCallID,
		"name":         result.Name,
		"content":      result.Content,
		"is_error":     result.IsError,
		"metadata":     metadata,
	}
	result.Metadata[toolMetadataParallelStepProgress] = stepProgress
	return result
}

func pendingToolResultsFromMetadata(raw interface{}) []session.ToolResult {
	switch value := raw.(type) {
	case []session.ToolResult:
		out := make([]session.ToolResult, len(value))
		copy(out, value)
		return out
	case []interface{}:
		out := make([]session.ToolResult, 0, len(value))
		for _, item := range value {
			result, ok := pendingToolResultFromUnknown(item)
			if ok {
				out = append(out, result)
			}
		}
		return out
	default:
		return nil
	}
}

func pendingToolResultFromUnknown(raw interface{}) (session.ToolResult, bool) {
	record, ok := raw.(map[string]interface{})
	if !ok {
		return session.ToolResult{}, false
	}
	toolCallID := stringFromUnknown(record["tool_call_id"])
	if toolCallID == "" {
		return session.ToolResult{}, false
	}
	return session.ToolResult{
		ToolCallID: toolCallID,
		Name:       stringFromUnknown(record["name"]),
		Content:    stringFromUnknown(record["content"]),
		IsError:    boolFromUnknown(record["is_error"]),
		Metadata:   mapFromUnknown(record["metadata"]),
		DurationMs: int64FromUnknown(record["duration_ms"]),
	}, true
}

func parallelStepProgressFromMetadata(raw interface{}) map[string]interface{} {
	out := make(map[string]interface{})
	if record, ok := raw.(map[string]interface{}); ok {
		for key, value := range record {
			if strings.TrimSpace(key) != "" {
				out[key] = value
			}
		}
	}
	return out
}

func cloneInterfaceMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func mapFromUnknown(raw interface{}) map[string]interface{} {
	if raw == nil {
		return nil
	}
	if record, ok := raw.(map[string]interface{}); ok {
		return cloneInterfaceMap(record)
	}
	return nil
}

func metadataStepKey(raw interface{}) string {
	switch value := raw.(type) {
	case int:
		if value > 0 {
			return fmt.Sprintf("%d", value)
		}
	case int64:
		if value > 0 {
			return fmt.Sprintf("%d", value)
		}
	case float64:
		if value > 0 && math.Trunc(value) == value {
			return fmt.Sprintf("%.0f", value)
		}
	case string:
		return strings.TrimSpace(value)
	}
	return ""
}

func stringFromUnknown(raw interface{}) string {
	value, _ := raw.(string)
	return strings.TrimSpace(value)
}

func boolFromUnknown(raw interface{}) bool {
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

func int64FromUnknown(raw interface{}) int64 {
	switch value := raw.(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		if math.Trunc(value) == value {
			return int64(value)
		}
	default:
		return 0
	}
	return 0
}

func toolProgressStatusIsError(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "error", "child_error":
		return true
	default:
		return false
	}
}
