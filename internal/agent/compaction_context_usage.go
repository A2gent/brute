package agent

import (
	"strings"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/session"
)

const (
	messageMetadataContextBeforePercent = "context_before_percent"
	messageMetadataContextAfterPercent  = "context_after_percent"
)

// estimateCompactedContextTokens mirrors the UI's rough four-characters-per-token estimate.
func estimateCompactedContextTokens(messages []session.Message, systemPrompt string) int {
	characters := utf8.RuneCountInString(strings.TrimSpace(systemPrompt))
	for _, message := range messages {
		characters += utf8.RuneCountInString(strings.TrimSpace(message.Content))
		for _, call := range message.ToolCalls {
			characters += utf8.RuneCountInString(call.Name)
			characters += utf8.RuneCountInString(string(call.Input))
		}
		for _, result := range message.ToolResults {
			characters += utf8.RuneCountInString(strings.TrimSpace(result.Content))
		}
	}
	return (characters + 3) / 4
}

func compactionContextUsageMetadata(beforeTokens float64, afterTokens, contextWindow int) map[string]interface{} {
	if contextWindow <= 0 {
		return nil
	}
	metadata := make(map[string]interface{}, 2)
	if beforeTokens > 0 {
		metadata[messageMetadataContextBeforePercent] = beforeTokens / float64(contextWindow) * 100
	}
	if afterTokens > 0 {
		metadata[messageMetadataContextAfterPercent] = float64(afterTokens) / float64(contextWindow) * 100
	}
	return metadata
}
