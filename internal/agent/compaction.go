package agent

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/google/uuid"
)

func (a *Agent) maybeCompactContext(ctx context.Context, sess *session.Session, step int) (llm.TokenUsage, bool, error) {
	cfg := a.resolveCompactionConfig()
	if !cfg.Enabled || sess == nil {
		return llm.TokenUsage{}, false, nil
	}

	currentTokens := metadataFloat(sess.Metadata, metadataCurrentContextTokens)
	if currentTokens <= 0 {
		return llm.TokenUsage{}, false, nil
	}

	usagePercent := (currentTokens / float64(cfg.ContextWindow)) * 100.0
	if usagePercent < cfg.TriggerPercent {
		return llm.TokenUsage{}, false, nil
	}

	// If the latest message is a user prompt awaiting the next response, keep it after compaction.
	var pendingUser *session.Message
	if len(sess.Messages) > 0 && sess.Messages[len(sess.Messages)-1].Role == "user" {
		last := sess.Messages[len(sess.Messages)-1]
		pendingUser = &last
		sess.Messages = sess.Messages[:len(sess.Messages)-1]
	}

	// Calculate which messages to summarize and which to keep
	// We want to keep the last N messages raw to preserve context
	const parserKeepMessages = 2

	activeMessages := a.getActiveConversationMessages(sess)
	if len(activeMessages) == 0 {
		if pendingUser != nil {
			sess.AddMessage(*pendingUser)
		}
		return llm.TokenUsage{}, false, nil
	}

	var messagesToSummarize []session.Message
	var messagesToKeep []session.Message

	// If we have enough messages, split them. Otherwise, summarize everything (fallback)
	// But usually we should have at least keep + 1 to make compaction worth it.
	// If len <= parserKeepMessages, we compact everything to avoid infinite loops if messages are just huge.
	if len(activeMessages) > parserKeepMessages {
		splitIdx := len(activeMessages) - parserKeepMessages
		messagesToSummarize = activeMessages[:splitIdx]
		messagesToKeep = activeMessages[splitIdx:]
	} else {
		messagesToSummarize = activeMessages
		messagesToKeep = []session.Message{}
	}

	request := a.buildCompactionRequestFromMessages(messagesToSummarize, cfg.Prompt)
	if len(request.Messages) == 0 {
		// Nothing to summarize? Should not happen if logic above is correct
		if pendingUser != nil {
			sess.AddMessage(*pendingUser)
		}
		return llm.TokenUsage{}, false, nil
	}

	logging.Info("Context compaction starting: session=%s messages_to_summarize=%d", sess.ID, len(messagesToSummarize))

	response, err := a.llmClient.Chat(ctx, request)
	if err != nil {
		logging.Warn("Context compaction LLM error: %v", err)
		if pendingUser != nil {
			sess.AddMessage(*pendingUser)
		}
		return llm.TokenUsage{}, false, fmt.Errorf("compaction LLM error: %w", err)
	}

	logging.Info("Context compaction LLM response: content_len=%d usage=%+v", len(response.Content), response.Usage)

	a.addTokenUsageMetadata(sess, response.Usage)
	metadataSetFloat(sess, metadataCurrentContextTokens, 0)

	compactionCount := int(metadataFloat(sess.Metadata, metadataCompactionCount)) + 1
	metadataSetFloat(sess, metadataCompactionCount, float64(compactionCount))
	metadataSetString(sess, metadataLastCompactionAt, time.Now().UTC().Format(time.RFC3339))

	summary := strings.TrimSpace(response.Content)
	if summary == "" {
		logging.Warn("Context compaction returned empty content, using fallback")
		summary = "Context was compacted to continue in a fresh window."
	}

	// Create summary message
	summaryMsg := session.Message{
		ID:        "", // will be generated
		Role:      "assistant",
		Content:   summary,
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			messageMetadataCompaction: true,
			"compaction_index":        compactionCount,
			"trigger_percent":         cfg.TriggerPercent,
			"triggered_at_step":       step,
		},
	}

	// Insert summary message BEFORE the kept messages.
	// We need to find the insertion point in the original sess.Messages.
	// activeMessages is a slice of sess.Messages.
	// The split point in activeMessages corresponds to `splitIdx`.
	// However, since we might have popped pendingUser, we just need to append Summary then Kept?
	// No, we want to maintain history.

	// Strategy: Rebuild sess.Messages.
	// [ ... (Old Inactive) ... , (Summarized Active) ... , (Kept Active) ... ]
	// becomes
	// [ ... (Old Inactive) ... , (Summarized Active) ... , NEW_SUMMARY, (Kept Active) ... ]

	// Finding the index of the first kept message in the main slice
	insertionIdx := len(sess.Messages) // Default to end
	if len(messagesToKeep) > 0 {
		// Find the index of the first kept message
		// Since activeMessages is a slice of sess.Messages, and messagesToKeep is a slice of activeMessages,
		// they share the same backing array (mostly). But safely, let's just use the ID or reference.
		firstKeptID := messagesToKeep[0].ID
		for i, m := range sess.Messages {
			if m.ID == firstKeptID {
				insertionIdx = i
				break
			}
		}
	}

	// Do the insertion
	// Extend slice
	sess.Messages = append(sess.Messages, session.Message{})
	copy(sess.Messages[insertionIdx+1:], sess.Messages[insertionIdx:])
	sess.Messages[insertionIdx] = summaryMsg

	// Ensure ID is generated
	if sess.Messages[insertionIdx].ID == "" {
		sess.Messages[insertionIdx].ID = uuid.New().String()
	}

	if pendingUser != nil {
		sess.AddMessage(*pendingUser)
	} else {
		// Add a synthetic user message to prompt the agent to continue working.
		// Without this, the LLM may interpret the compaction summary as a final response
		// and return without tool calls, causing premature completion.
		sess.AddMessage(session.Message{
			Role:      "user",
			Content:   "Continue with the task based on the summary above.",
			Timestamp: time.Now(),
			Metadata: map[string]interface{}{
				"synthetic_continuation": true,
			},
		})
	}

	// A provider-side session still contains the pre-compaction transcript. Start a
	// fresh session so the summary and kept messages become its only history.
	// Empty tombstones keep a concurrent state merge from restoring the pre-compaction
	// provider session if persisting this reset fails.
	metadataSetString(sess, metadataProviderSessionCursor, "")
	metadataSetString(sess, metadataProviderSessionIdentity, "")

	if err := a.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to save compacted session state: %v", err)
	}

	logging.Info("Context compaction completed: session=%s current_tokens=%.0f threshold=%.1f%% kept=%d", sess.ID, currentTokens, cfg.TriggerPercent, len(messagesToKeep))
	return response.Usage, true, nil
}

func (a *Agent) resolveCompactionConfig() compactionConfig {
	contextWindow := a.config.ContextWindow
	if contextWindow <= 0 {
		return compactionConfig{Enabled: false}
	}

	trigger := a.config.CompactionTriggerPercent
	if trigger <= 0 {
		trigger = defaultCompactionTriggerPct
	}
	if envTrigger := strings.TrimSpace(os.Getenv(envCompactionTriggerPercent)); envTrigger != "" {
		if parsed, err := strconv.ParseFloat(envTrigger, 64); err == nil {
			trigger = parsed
		}
	}
	if trigger <= 0 {
		return compactionConfig{Enabled: false}
	}
	if trigger > 100 {
		trigger = 100
	}

	prompt := strings.TrimSpace(a.config.CompactionPrompt)
	if envPrompt := strings.TrimSpace(os.Getenv(envCompactionPrompt)); envPrompt != "" {
		prompt = envPrompt
	}
	if prompt == "" {
		prompt = defaultCompactionPrompt
	}

	return compactionConfig{
		Enabled:        true,
		ContextWindow:  contextWindow,
		TriggerPercent: trigger,
		Prompt:         prompt,
	}
}
