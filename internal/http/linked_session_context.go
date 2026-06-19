package http

import (
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/session"
)

const (
	linkedContinuationOriginalLimit      = 2400
	linkedContinuationTaskProgressLimit  = 2400
	linkedContinuationCompactionLimit    = 3000
	linkedContinuationRecentMessageLimit = 1000
	linkedContinuationToolResultLimit    = 500
	linkedContinuationRecentMessageCount = 10
	linkedContinuationPromptLimit        = 12000
	linkedContinuationFinalInstruction   = "\n\nContinue from this compact context. Do not assume the full parent transcript is present; inspect the repository and current files as needed before changing code."
	linkedContinuationTruncationNotice   = "\n\n[Linked continuation context truncated to fit the compact handoff budget.]"
)

type linkedContinuationContext struct {
	Prompt             string
	ParentSessionID    string
	SourceMessageID    string
	ParentMessageCount int
	RecentMessageCount int
	Truncated          bool
}

func buildLinkedContinuationContext(parent *session.Session, request string) linkedContinuationContext {
	if parent == nil {
		return linkedContinuationContext{Prompt: strings.TrimSpace(request)}
	}

	sourceMessage, hasSource := firstLinkedContextUserMessage(parent)
	sourceMessageID := ""
	if hasSource {
		sourceMessageID = sourceMessage.ID
	}

	request = strings.TrimSpace(request)
	if request == "" {
		request = fmt.Sprintf("Continue the implementation from parent session %q (%s).", linkedContextSessionTitle(parent), parent.ID)
	}

	recentLog, recentCount := linkedContinuationRecentLog(parent.Messages, sourceMessageID)
	compactionSummary, hasCompactionSummary := latestLinkedContextCompactionSummary(parent.Messages)

	var b strings.Builder
	b.WriteString(request)
	b.WriteString("\n\nParent session context (compact; full transcript omitted intentionally):\n")
	b.WriteString(fmt.Sprintf("- Parent session: %s\n", parent.ID))
	b.WriteString(fmt.Sprintf("- Parent title: %s\n", linkedContextSessionTitle(parent)))
	b.WriteString(fmt.Sprintf("- Parent status: %s\n", parent.Status))
	b.WriteString(fmt.Sprintf("- Parent messages: %d\n", len(parent.Messages)))
	if sourceMessageID != "" {
		b.WriteString(fmt.Sprintf("- Original message id: %s\n", sourceMessageID))
	}

	if hasSource {
		original, truncated := truncateLinkedContextText(sourceMessage.Content, linkedContinuationOriginalLimit)
		b.WriteString("\nOriginal user message:\n")
		b.WriteString(original)
		if truncated {
			b.WriteString("\n[Original message truncated.]\n")
		}
		b.WriteString("\n")
	}

	taskProgress := strings.TrimSpace(parent.TaskProgress)
	if taskProgress != "" {
		progress, truncated := truncateLinkedContextText(taskProgress, linkedContinuationTaskProgressLimit)
		b.WriteString("\nTask progress from parent:\n")
		b.WriteString(progress)
		if truncated {
			b.WriteString("\n[Task progress truncated.]\n")
		}
		b.WriteString("\n")
	}

	if hasCompactionSummary {
		summary, truncated := truncateLinkedContextText(compactionSummary, linkedContinuationCompactionLimit)
		b.WriteString("\nLatest parent compaction summary:\n")
		b.WriteString(summary)
		if truncated {
			b.WriteString("\n[Compaction summary truncated.]\n")
		}
		b.WriteString("\n")
	}

	if recentLog != "" {
		b.WriteString("\nRecent parent log (bounded):\n")
		b.WriteString(recentLog)
		b.WriteString("\n")
	}

	body := b.String()
	prompt, truncated := truncateLinkedContextText(body+linkedContinuationFinalInstruction, linkedContinuationPromptLimit)
	if truncated {
		bodyLimit := linkedContinuationPromptLimit - len([]rune(linkedContinuationFinalInstruction)) - len([]rune(linkedContinuationTruncationNotice))
		if bodyLimit < 0 {
			bodyLimit = 0
		}
		compactBody, _ := truncateLinkedContextText(body, bodyLimit)
		prompt = compactBody + linkedContinuationFinalInstruction + linkedContinuationTruncationNotice
	}

	return linkedContinuationContext{
		Prompt:             prompt,
		ParentSessionID:    parent.ID,
		SourceMessageID:    sourceMessageID,
		ParentMessageCount: len(parent.Messages),
		RecentMessageCount: recentCount,
		Truncated:          truncated,
	}
}

func linkedContinuationSessionMetadata(ctx linkedContinuationContext) map[string]interface{} {
	metadata := map[string]interface{}{
		"linked_context_mode":           "compact",
		"linked_parent_message_count":   ctx.ParentMessageCount,
		"linked_recent_message_count":   ctx.RecentMessageCount,
		"linked_context_prompt_chars":   len([]rune(ctx.Prompt)),
		"linked_context_prompt_limited": ctx.Truncated,
	}
	if ctx.ParentSessionID != "" {
		metadata["linked_source_session_id"] = ctx.ParentSessionID
	}
	if ctx.SourceMessageID != "" {
		metadata["linked_source_message_id"] = ctx.SourceMessageID
	}
	return metadata
}

func linkedContinuationMessageMetadata(ctx linkedContinuationContext) map[string]interface{} {
	metadata := map[string]interface{}{
		"linked_context": true,
		"context_mode":   "compact",
	}
	if ctx.ParentSessionID != "" {
		metadata["source_session_id"] = ctx.ParentSessionID
	}
	if ctx.SourceMessageID != "" {
		metadata["source_message_id"] = ctx.SourceMessageID
	}
	if ctx.Truncated {
		metadata["context_truncated"] = true
	}
	return metadata
}

func applyLinkedContinuationSessionMetadata(sess *session.Session, ctx linkedContinuationContext) {
	if sess == nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	for key, value := range linkedContinuationSessionMetadata(ctx) {
		sess.Metadata[key] = value
	}
}

func firstLinkedContextUserMessage(sess *session.Session) (session.Message, bool) {
	if sess == nil {
		return session.Message{}, false
	}
	for _, msg := range sess.Messages {
		if msg.Role != "user" || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		if linkedContextMetadataBool(msg.Metadata, "internal_handoff") || linkedContextMetadataBool(msg.Metadata, "synthetic_continuation") {
			continue
		}
		return msg, true
	}
	return session.Message{}, false
}

func latestLinkedContextCompactionSummary(messages []session.Message) (string, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "assistant" || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		if linkedContextMetadataBool(msg.Metadata, "context_compaction") {
			return strings.TrimSpace(msg.Content), true
		}
	}
	return "", false
}

func linkedContinuationRecentLog(messages []session.Message, sourceMessageID string) (string, int) {
	if len(messages) == 0 {
		return "", 0
	}
	start := len(messages) - linkedContinuationRecentMessageCount
	if start < 0 {
		start = 0
	}

	var lines []string
	for _, msg := range messages[start:] {
		if msg.ID != "" && msg.ID == sourceMessageID {
			continue
		}
		line := linkedContextMessageLogEntry(msg)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), len(lines)
}

func linkedContextMessageLogEntry(msg session.Message) string {
	role := strings.TrimSpace(msg.Role)
	if role == "" {
		role = "message"
	}

	var parts []string
	content := strings.TrimSpace(msg.Content)
	if content != "" {
		snippet, truncated := truncateLinkedContextText(compactLinkedContextWhitespace(content), linkedContinuationRecentMessageLimit)
		if truncated {
			snippet += " [truncated]"
		}
		parts = append(parts, fmt.Sprintf("- %s: %s", linkedContextRoleLabel(role), snippet))
	}

	if len(msg.ToolCalls) > 0 {
		names := make([]string, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			name := strings.TrimSpace(call.Name)
			if name == "" {
				name = "tool"
			}
			names = append(names, name)
		}
		if len(names) > 0 {
			parts = append(parts, fmt.Sprintf("- %s tool calls: %s", linkedContextRoleLabel(role), strings.Join(names, ", ")))
		}
	}

	for _, result := range msg.ToolResults {
		name := strings.TrimSpace(result.Name)
		if name == "" {
			name = strings.TrimSpace(result.ToolCallID)
		}
		if name == "" {
			name = "tool"
		}
		status := "ok"
		if result.IsError {
			status = "error"
		}
		snippet := compactLinkedContextWhitespace(result.Content)
		if snippet == "" {
			parts = append(parts, fmt.Sprintf("- Tool result %s (%s, %d chars)", name, status, len([]rune(result.Content))))
			continue
		}
		short, truncated := truncateLinkedContextText(snippet, linkedContinuationToolResultLimit)
		if truncated {
			short += " [truncated]"
		}
		parts = append(parts, fmt.Sprintf("- Tool result %s (%s, %d chars): %s", name, status, len([]rune(result.Content)), short))
	}

	return strings.Join(parts, "\n")
}

func linkedContextSessionTitle(sess *session.Session) string {
	if sess == nil {
		return "parent session"
	}
	title := strings.TrimSpace(sess.Title)
	if title != "" {
		return title
	}
	if sess.ID == "" {
		return "parent session"
	}
	if len(sess.ID) <= 8 {
		return "Session " + sess.ID
	}
	return "Session " + sess.ID[:8]
}

func linkedContextRoleLabel(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return "Message"
	}
	runes := []rune(role)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

func linkedContextMetadataBool(metadata map[string]interface{}, key string) bool {
	if metadata == nil {
		return false
	}
	raw, ok := metadata[key]
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

func compactLinkedContextWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func truncateLinkedContextText(value string, maxRunes int) (string, bool) {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return "", value != ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value, false
	}
	return string(runes[:maxRunes]), true
}
