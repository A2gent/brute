package agent

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/google/uuid"
)

func (a *Agent) maybeCompactContext(ctx context.Context, sess *session.Session, step int, observers ...func(Event)) (llm.TokenUsage, bool, error) {
	return a.compactContext(ctx, sess, step, false, true, observers...)
}

// CompactContext summarizes the active conversation on demand without adding a
// synthetic continuation prompt, so the next agent run remains user-initiated.
func (a *Agent) CompactContext(ctx context.Context, sess *session.Session) error {
	if sess == nil || a.sessionManager == nil {
		return fmt.Errorf("session is unavailable for context compaction")
	}
	ctx, release, err := a.sessionManager.AcquireRun(ctx, sess.ID)
	if err != nil {
		return err
	}
	defer release()

	_, compacted, err := a.compactContext(fallback.WithSessionBoundary(ctx), sess, 0, true, false)
	if err != nil {
		return err
	}
	if !compacted {
		return fmt.Errorf("no conversation context to compact")
	}
	return nil
}

func (a *Agent) compactContext(
	ctx context.Context,
	sess *session.Session,
	step int,
	force bool,
	continueAfter bool,
	observers ...func(Event),
) (llm.TokenUsage, bool, error) {
	cfg := a.resolveCompactionConfig()
	if !cfg.Enabled || sess == nil {
		return llm.TokenUsage{}, false, nil
	}

	currentTokens := metadataFloat(sess.Metadata, metadataCurrentContextTokens)
	if currentTokens <= 0 && !force {
		return llm.TokenUsage{}, false, nil
	}

	usagePercent := (currentTokens / float64(cfg.ContextWindow)) * 100.0
	if !force && usagePercent < cfg.TriggerPercent {
		return llm.TokenUsage{}, false, nil
	}

	// Work on an isolated candidate: empty summaries, cancellation and save failures
	// must not mutate the active context or archived transcript.
	original := sess
	sess = cloneTransitionSession(sess)

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
		// Keep whole tool batches together, never start the retained tail with a result.
		for splitIdx > 0 && activeMessages[splitIdx].Role == "tool" {
			splitIdx--
		}
		messagesToSummarize = activeMessages[:splitIdx]
		messagesToKeep = activeMessages[splitIdx:]
	} else {
		messagesToSummarize = activeMessages
		messagesToKeep = []session.Message{}
	}

	// original still holds the pending user prompt, which anchors the relevance task.
	filtered, jevStats := filterHistoryWithJev(ctx, a.config.RelevanceGate, compactionRelevanceTask(original), messagesToSummarize)
	request := a.buildCompactionRequestFromMessages(filtered, cfg.Prompt)
	if len(request.Messages) == 0 {
		// Nothing to summarize? Should not happen if logic above is correct
		if pendingUser != nil {
			sess.AddMessage(*pendingUser)
		}
		return llm.TokenUsage{}, false, nil
	}

	logging.Info("Context compaction starting: session=%s messages_to_summarize=%d", sess.ID, len(messagesToSummarize))

	for _, onEvent := range observers {
		if onEvent != nil {
			onEvent(Event{Type: EventProviderTrace, Step: step, Provider: &ProviderTraceEvent{
				Provider: a.config.Provider, Model: a.config.Model,
				Phase: "compaction_started", Reason: "Compacting context...",
			}})
		}
	}
	llmStartedAt := time.Now()
	outcome := "error"
	timingRecorded := false
	recordTiming := func() {
		if !timingRecorded {
			// The candidate may be discarded on failure, but its timing must not be.
			recordAuxiliaryCall(original, "compaction", llmStartedAt, outcome)
			for _, suffix := range []string{"attempt_count", "total_duration_ms", "last_duration_ms", "last_started_at", "last_outcome"} {
				key := "compaction_" + suffix
				sess.Metadata[key] = original.Metadata[key]
			}
			timingRecorded = true
		}
	}
	defer recordTiming()
	response, err := llm.WithProvenance(a.llmClient, a.config.Provider, a.config.Model).Chat(ctx, request)
	if err != nil {
		logging.Warn("Context compaction LLM error: %v", err)
		if pendingUser != nil {
			sess.AddMessage(*pendingUser)
		}
		return llm.TokenUsage{}, false, fmt.Errorf("compaction LLM error: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return llm.TokenUsage{}, false, err
	}
	if response == nil || strings.TrimSpace(response.Content) == "" || len(response.ToolCalls) > 0 {
		return llm.TokenUsage{}, false, fmt.Errorf("compaction returned no usable summary")
	}

	logging.Info("Context compaction LLM response: content_len=%d usage=%+v", len(response.Content), response.Usage)

	a.addTokenUsageMetadata(sess, response.Usage)
	recordLLMRequestMetadata(sess, llmStartedAt, a.config.Provider, a.config.Model, response.Usage)
	metadataSetFloat(sess, metadataCurrentContextTokens, 0)

	compactionCount := int(metadataFloat(sess.Metadata, metadataCompactionCount)) + 1
	metadataSetFloat(sess, metadataCompactionCount, float64(compactionCount))
	metadataSetString(sess, metadataLastCompactionAt, time.Now().UTC().Format(time.RFC3339))

	summary := strings.TrimSpace(response.Content)

	// Create summary message
	summaryMsg := session.Message{
		Provenance: response.Provenance,
		ID:         "", // will be generated
		Role:       "assistant",
		Content:    summary,
		Timestamp:  time.Now(),
		Metadata: map[string]interface{}{
			messageMetadataCompaction: true,
			"compaction_index":        compactionCount,
			"trigger_percent":         cfg.TriggerPercent,
			"triggered_at_step":       step,
		},
	}
	if jevStats.Used {
		for key, value := range jevStats.metadata() {
			summaryMsg.Metadata[key] = value
		}
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
	} else if continueAfter {
		// Automatic compaction runs inside an active task and needs a prompt to resume it.
		sess.AddMessage(session.Message{
			Role:      "user",
			Content:   "Continue with the task based on the summary above.",
			Timestamp: time.Now(),
			Metadata: map[string]interface{}{
				"synthetic_continuation": true,
			},
		})
	}

	beforeMessages := activeMessages
	if pendingUser != nil {
		beforeMessages = append(append([]session.Message(nil), activeMessages...), *pendingUser)
	}
	beforeContextTokens := currentTokens
	if beforeContextTokens <= 0 {
		beforeContextTokens = float64(estimateCompactedContextTokens(beforeMessages, a.config.SystemPrompt))
	}

	// Persist the pre-compaction measurement and a text-based estimate of the rebuilt active context.
	activeContext := a.getActiveConversationMessages(sess)
	for key, value := range compactionContextUsageMetadata(
		beforeContextTokens,
		estimateCompactedContextTokens(activeContext, a.config.SystemPrompt),
		cfg.ContextWindow,
	) {
		sess.Messages[insertionIdx].Metadata[key] = value
	}

	// A provider-side session still contains the pre-compaction transcript. Start a
	// fresh session so the summary and kept messages become its only history.
	// Empty tombstones keep a concurrent state merge from restoring the pre-compaction
	// provider session if persisting this reset fails.
	metadataSetString(sess, metadataLastResponseID, "")
	metadataSetString(sess, metadataProviderSessionCursor, "")
	metadataSetString(sess, metadataProviderSessionIdentity, "")

	outcome = "success"
	recordTiming()
	if err := a.sessionManager.Save(sess); err != nil {
		metadataSetString(original, "compaction_last_outcome", "save_error")
		return llm.TokenUsage{}, false, fmt.Errorf("save compacted context: %w", err)
	}

	*original = *sess

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
