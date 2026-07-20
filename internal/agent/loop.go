package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

// loop implements the main agentic loop
// Returns the response content and total token usage
func (a *Agent) loop(ctx context.Context, sess *session.Session, onEvent func(Event)) (string, llm.TokenUsage, error) {
	step := 0
	totalUsage := llm.TokenUsage{}
	emptyFinalResponseRetries := 0
	transientUserPrompt := ""

	// Add session ID to context for tools that need it (e.g., question tool)
	ctx = context.WithValue(ctx, "session_id", sess.ID)

	// Clean up incomplete tool calls before starting
	a.cleanupIncompleteToolCalls(sess)

	for {
		// Check context - distinguish between user cancellation and timeouts
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				// Explicit user cancellation (e.g., user clicked cancel, closed browser)
				// Pause immediately - user wants to stop
				logging.Info("User cancelled session %s", sess.ID)
				sess.SetStatus(session.StatusPaused)
				a.sessionManager.Save(sess)
				return "", totalUsage, ctx.Err()
			}
			// For context.DeadlineExceeded, we continue and let the agent see tool errors
			// The agent can then decide whether to retry or give up
			logging.Info("Context deadline exceeded for session %s, continuing to let agent handle errors", sess.ID)
		}

		// Check step limit
		if step >= a.config.MaxSteps {
			content, usage, err := a.finalizeAfterStepLimit(ctx, sess, step, onEvent)
			totalUsage.InputTokens += usage.InputTokens
			totalUsage.OutputTokens += usage.OutputTokens
			totalUsage.CachedInputTokens += usage.CachedInputTokens
			totalUsage.ReasoningTokens += usage.ReasoningTokens
			return content, totalUsage, err
		}

		step++
		logging.Debug("Agent step %d/%d", step, a.config.MaxSteps)

		// Compact conversation before the next normal step once threshold is reached.
		compactionUsage, compacted, err := a.maybeCompactContext(ctx, sess, step)
		if err != nil {
			logging.Warn("Context compaction failed (continuing without compaction): %v", err)
		} else if compacted {
			totalUsage.InputTokens += compactionUsage.InputTokens
			totalUsage.OutputTokens += compactionUsage.OutputTokens
			totalUsage.CachedInputTokens += compactionUsage.CachedInputTokens
			totalUsage.ReasoningTokens += compactionUsage.ReasoningTokens
		}

		// Build chat request. Reload first so non-blocking user notes
		// injected while the current request/tool batch was running are visible
		// to the next LLM turn.
		a.mergeFreshSessionState(sess)
		request := a.buildRequest(sess)
		if strings.TrimSpace(transientUserPrompt) != "" {
			request.Messages = append(request.Messages, llm.Message{
				Role:    "user",
				Content: strings.TrimSpace(transientUserPrompt),
			})
			transientUserPrompt = ""
		}
		appendStepLimitWarning(request, step, a.config.MaxSteps)

		// Call LLM (streaming when supported)
		llmStart := time.Now()
		response, err := a.callLLM(ctx, request, step, onEvent)
		llmCompleted := time.Now()
		if err != nil {
			sess.SetStatus(session.StatusFailed)
			a.sessionManager.Save(sess)
			return "", totalUsage, fmt.Errorf("LLM error: %w", err)
		}
		assistantMetadata := llmTimingMetadata(llmStart, llmCompleted, a.config.Provider, a.config.Model)
		if a.config.UsePreviousResponse && strings.TrimSpace(response.ResponseID) != "" {
			assistantMetadata[messageMetadataResponseID] = strings.TrimSpace(response.ResponseID)
			metadataSetString(sess, metadataLastResponseID, strings.TrimSpace(response.ResponseID))
		}
		if a.config.UseProviderSession && strings.TrimSpace(response.ProviderSessionCursor) != "" {
			cursor := strings.TrimSpace(response.ProviderSessionCursor)
			rawCursor := cursor
			if identity := strings.TrimSpace(a.config.ProviderSessionIdentity); identity != "" {
				rawCursor = claudecli.UnbindProviderSessionCursor(identity, cursor)
			}
			assistantMetadata[messageMetadataProviderSessionCursor] = rawCursor
			metadataSetString(sess, metadataProviderSessionCursor, rawCursor)
			if identity := strings.TrimSpace(a.config.ProviderSessionIdentity); identity != "" {
				assistantMetadata[messageMetadataProviderSessionIdentity] = identity
				metadataSetString(sess, metadataProviderSessionIdentity, identity)
			}
		}

		// Accumulate token usage
		totalUsage.InputTokens += response.Usage.InputTokens
		totalUsage.OutputTokens += response.Usage.OutputTokens
		totalUsage.CachedInputTokens += response.Usage.CachedInputTokens
		totalUsage.ReasoningTokens += response.Usage.ReasoningTokens
		a.addTokenUsageMetadata(sess, response.Usage)

		// Check if we have tool calls
		if len(response.ToolCalls) == 0 {
			// No tool calls - agent is done unless the user injected new context
			// while this LLM request was in flight. In that case, persist the
			// assistant reply, append the injected note after it, and run one more
			// turn so the backend flow actually incorporates the new user input.
			finalContent := strings.TrimSpace(response.Content)
			if finalContent == "" {
				if emptyFinalResponseRetries < emptyFinalResponseMaxRetries {
					emptyFinalResponseRetries++
					transientUserPrompt = emptyFinalResponseRetryPrompt
					logging.Warn("Model returned empty final response; retrying once for session=%s step=%d", sess.ID, step)
					continue
				}
				message := "Model returned an empty final response without tool calls after a retry. Check provider health, quota/usage, and child-agent logs; an upstream CLI or proxy may have failed without surfacing a normal error."
				sess.AddAssistantMessageWithMetadata(message, nil, llmTimingMetadata(llmStart, llmCompleted, a.config.Provider, a.config.Model, map[string]interface{}{
					"empty_final_response": true,
				}))
				sess.SetStatus(session.StatusFailed)
				a.sessionManager.Save(sess)
				return "", totalUsage, errors.New(message)
			}
			emptyFinalResponseRetries = 0
			sess.AddAssistantMessageWithImagesAndMetadata(finalContent, llmImagesToSession(response.Images), nil, assistantMetadata)
			if a.mergeFreshSessionState(sess) {
				if err := a.sessionManager.Save(sess); err != nil {
					_ = err
				}
				if onEvent != nil {
					onEvent(Event{Type: EventStepCompleted, Step: step})
				}
				continue
			}
			sess.SetStatus(session.StatusCompleted)
			a.sessionManager.Save(sess)
			if onEvent != nil {
				onEvent(Event{Type: EventStepCompleted, Step: step})
			}
			return finalContent, totalUsage, nil
		}

		emptyFinalResponseRetries = 0

		// Convert tool calls for session storage
		sessionToolCalls := make([]session.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			inputRaw := []byte(strings.TrimSpace(tc.Input))
			if len(inputRaw) == 0 || !json.Valid(inputRaw) {
				// Keep session/history encodable even if provider streamed malformed args.
				escaped, _ := json.Marshal(tc.Input)
				inputRaw = escaped
			}
			sessionToolCalls = append(sessionToolCalls, session.ToolCall{
				ID:               tc.ID,
				Name:             tc.Name,
				Input:            inputRaw,
				ThoughtSignature: tc.ThoughtSignature,
			})
		}

		// Add assistant message with tool calls
		sess.AddAssistantMessageWithImagesAndMetadata(response.Content, llmImagesToSession(response.Images), sessionToolCalls, assistantMetadata)
		// Persist the assistant tool-call message before executing tools so
		// async tools that park the session (for example webhook-backed image
		// generation) still leave behind a valid transcript for resume.
		if err := a.sessionManager.Save(sess); err != nil {
			_ = err
		}

		// Execute tools
		if onEvent != nil {
			toolCallEvents := make([]ToolCallEvent, len(response.ToolCalls))
			for i, tc := range response.ToolCalls {
				toolCallEvents[i] = ToolCallEvent{
					ID:               tc.ID,
					Name:             tc.Name,
					Input:            tc.Input,
					ThoughtSignature: tc.ThoughtSignature,
				}
			}
			onEvent(Event{Type: EventToolExecuting, Step: step, ToolCalls: toolCallEvents})
		}
		toolCtx := ctx
		var progressSaveMu sync.Mutex
		if onEvent != nil {
			toolCtx = tools.WithProgressCallback(ctx, func(progress tools.ProgressEvent) {
				progressSaveMu.Lock()
				a.mergeFreshSessionState(sess)
				if recordPendingToolProgress(sess, progress) {
					sess.UpdatedAt = time.Now()
					if err := a.sessionManager.Save(sess); err != nil {
						logging.Warn("Failed to persist tool progress for session %s: %v", sess.ID, err)
					}
				}
				progressSaveMu.Unlock()

				onEvent(Event{
					Type: EventToolProgress,
					Step: step,
					ToolProgress: &ToolProgressEvent{
						ToolCallID: progress.ToolCallID,
						ToolName:   progress.ToolName,
						Status:     progress.Status,
						Content:    progress.Content,
						Metadata:   progress.Metadata,
					},
				})
			})
		}
		toolResults := a.toolManager.ExecuteParallel(toolCtx, response.ToolCalls)

		// Convert results
		sessionResults := make([]session.ToolResult, len(toolResults))
		for i, tr := range toolResults {
			sessionResults[i] = session.ToolResult{
				ToolCallID: tr.ToolCallID,
				Content:    tr.Content,
				IsError:    tr.IsError,
				Metadata:   tr.Metadata,
				Name:       tr.Name,
				DurationMs: tr.DurationMs,
			}
		}

		// Add tool results to session
		sess.AddToolResult(sessionResults)
		if clearPendingToolProgressMetadata(sess) {
			sess.UpdatedAt = time.Now()
		}

		// Merge in any user notes that were injected while tools were running
		// before saving this step, otherwise the agent's in-memory transcript
		// could overwrite them.
		a.mergeFreshSessionState(sess)

		// Reload session to check if status was changed by tools (e.g., question tool)
		// Also sync any fields that tools may have updated (e.g., task_progress)
		// IMPORTANT: Do this BEFORE Save() so we can detect status changes made by tools
		freshSess, reloadErr := a.sessionManager.Get(sess.ID)
		if reloadErr == nil {
			// Sync task_progress from DB (may have been updated by session_task_progress tool)
			sess.TaskProgress = freshSess.TaskProgress

			if freshSess.Status == session.StatusInputRequired {
				logging.Info("Session %s requires user input (detected after tool execution), pausing", sess.ID)
				// Keep caller-visible session state in sync with DB state set by tools.
				sess.Status = freshSess.Status
				sess.Metadata = freshSess.Metadata
				sess.TaskProgress = freshSess.TaskProgress
				sess.UpdatedAt = freshSess.UpdatedAt
				// Don't save the local sess changes - use the fresh one with input_required status
				if onEvent != nil {
					onEvent(Event{Type: EventToolCompleted, Step: step})
					onEvent(Event{Type: EventStepCompleted, Step: step})
				}
				return "", totalUsage, nil
			}
			if freshSess.Status == session.StatusWaitingExternal || hasExternalWaitResult(sessionResults) {
				logging.Info("Session %s is waiting for external callback, parking agent loop", sess.ID)
				sess.Status = session.StatusWaitingExternal
				sess.Metadata = freshSess.Metadata
				sess.TaskProgress = freshSess.TaskProgress
				sess.UpdatedAt = freshSess.UpdatedAt
				if onEvent != nil {
					onEvent(Event{Type: EventToolCompleted, Step: step})
					onEvent(Event{Type: EventStepCompleted, Step: step})
				}
				return "", totalUsage, nil
			}
		}

		// Save session after each step
		if err := a.sessionManager.Save(sess); err != nil {
			// Silently continue on save errors
			_ = err
		}

		if onEvent != nil {
			onEvent(Event{Type: EventToolCompleted, Step: step})
			onEvent(Event{Type: EventStepCompleted, Step: step})
		}
	}
}

func (a *Agent) finalizeAfterStepLimit(ctx context.Context, sess *session.Session, step int, onEvent func(Event)) (string, llm.TokenUsage, error) {
	request := a.buildRequest(sess)
	request.Tools = nil
	request.Messages = append(request.Messages, llm.Message{
		Role:    "user",
		Content: stepLimitFinalizationPrompt,
	})

	llmStart := time.Now()
	response, err := a.callLLM(ctx, request, step+1, onEvent)
	llmCompleted := time.Now()
	if err != nil {
		message := fmt.Sprintf("Agent stopped after reaching maximum step limit (%d), and the finalization request failed: %v", a.config.MaxSteps, err)
		sess.AddAssistantMessageWithMetadata(message, nil, stepLimitFailureMetadata(a.config.MaxSteps, llmStart, llmCompleted, a.config.Provider, a.config.Model, map[string]interface{}{
			"finalization_error": err.Error(),
		}))
		sess.SetStatus(session.StatusFailed)
		a.sessionManager.Save(sess)
		return "", llm.TokenUsage{}, errors.New(message)
	}

	usage := response.Usage
	a.addTokenUsageMetadata(sess, usage)
	metadata := stepLimitFailureMetadata(a.config.MaxSteps, llmStart, llmCompleted, a.config.Provider, a.config.Model, map[string]interface{}{
		"finalized_after_step_limit": true,
	})
	if a.config.UsePreviousResponse && strings.TrimSpace(response.ResponseID) != "" {
		metadata[messageMetadataResponseID] = strings.TrimSpace(response.ResponseID)
		metadataSetString(sess, metadataLastResponseID, strings.TrimSpace(response.ResponseID))
	}
	if a.config.UseProviderSession && strings.TrimSpace(response.ProviderSessionCursor) != "" {
		cursor := strings.TrimSpace(response.ProviderSessionCursor)
		rawCursor := cursor
		if identity := strings.TrimSpace(a.config.ProviderSessionIdentity); identity != "" {
			rawCursor = claudecli.UnbindProviderSessionCursor(identity, cursor)
		}
		metadata[messageMetadataProviderSessionCursor] = rawCursor
		metadataSetString(sess, metadataProviderSessionCursor, rawCursor)
		if identity := strings.TrimSpace(a.config.ProviderSessionIdentity); identity != "" {
			metadata[messageMetadataProviderSessionIdentity] = identity
			metadataSetString(sess, metadataProviderSessionIdentity, identity)
		}
	}

	if len(response.ToolCalls) > 0 {
		message := fmt.Sprintf("Agent stopped after reaching maximum step limit (%d), and the finalization request attempted to call tools instead of producing a final answer.", a.config.MaxSteps)
		metadata["finalization_tool_call_count"] = len(response.ToolCalls)
		sess.AddAssistantMessageWithMetadata(message, nil, metadata)
		sess.SetStatus(session.StatusFailed)
		a.sessionManager.Save(sess)
		return "", usage, errors.New(message)
	}

	finalContent := strings.TrimSpace(response.Content)
	if finalContent == "" {
		message := fmt.Sprintf("Agent stopped after reaching maximum step limit (%d), and the finalization request returned an empty answer.", a.config.MaxSteps)
		metadata["empty_final_response"] = true
		sess.AddAssistantMessageWithMetadata(message, nil, metadata)
		sess.SetStatus(session.StatusFailed)
		a.sessionManager.Save(sess)
		return "", usage, errors.New(message)
	}

	sess.AddAssistantMessageWithImagesAndMetadata(finalContent, llmImagesToSession(response.Images), nil, metadata)
	sess.SetStatus(session.StatusCompleted)
	a.sessionManager.Save(sess)
	if onEvent != nil {
		onEvent(Event{Type: EventStepCompleted, Step: step + 1})
	}
	return finalContent, usage, nil
}

func appendStepLimitWarning(request *llm.ChatRequest, currentStep, maxSteps int) {
	if request == nil || maxSteps <= 0 {
		return
	}
	remaining := maxSteps - currentStep + 1
	if remaining < 1 || remaining > stepLimitWarningThreshold {
		return
	}
	request.Messages = append(request.Messages, llm.Message{
		Role:    "user",
		Content: fmt.Sprintf(stepLimitWarningPrompt, remaining),
	})
}

func stepLimitFailureMetadata(maxSteps int, startedAt, completedAt time.Time, provider, model string, extra map[string]interface{}) map[string]interface{} {
	metadata := llmTimingMetadata(startedAt, completedAt, provider, model, extra)
	metadata["max_steps_exceeded"] = true
	metadata["max_steps"] = maxSteps
	return metadata
}

// cleanupIncompleteToolCalls removes assistant messages with tool calls that don't have corresponding tool results
// This can happen when the user interrupts a tool execution
func (a *Agent) cleanupIncompleteToolCalls(sess *session.Session) {
	if len(sess.Messages) == 0 {
		return
	}

	// Find the last assistant message with tool calls
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		msg := sess.Messages[i]

		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// Check if there's a following tool message with results
			hasResults := false
			if i+1 < len(sess.Messages) && sess.Messages[i+1].Role == "tool" {
				hasResults = true
			}

			if !hasResults {
				// Remove this incomplete assistant message
				logging.Warn("Removing incomplete tool call message (no results)")
				sess.Messages = append(sess.Messages[:i], sess.Messages[i+1:]...)
				// Continue checking in case there are more
				continue
			}
		}
	}
}
func (a *Agent) mergeFreshSessionState(sess *session.Session) bool {
	if a == nil || a.sessionManager == nil || sess == nil || sess.ID == "" {
		return false
	}
	fresh, err := a.sessionManager.Get(sess.ID)
	if err != nil || fresh == nil {
		return false
	}

	changed := false
	if len(fresh.Messages) > len(sess.Messages) && sessionMessagePrefixMatches(fresh.Messages, sess.Messages) {
		sess.Messages = append(sess.Messages, fresh.Messages[len(sess.Messages):]...)
		changed = true
	} else {
		// WHY: users can inject notes while an LLM call or tool batch is running. The
		// active agent has an older in-memory transcript; without merging these DB-only
		// messages before Save(), the storage layer treats them as stale and deletes
		// them. Append only injected user notes here so we preserve the valid
		// assistant-tool-result ordering while still making the user's note visible to
		// the next LLM turn.
		existing := make(map[string]struct{}, len(sess.Messages))
		for _, msg := range sess.Messages {
			if msg.ID != "" {
				existing[msg.ID] = struct{}{}
			}
		}
		for _, msg := range fresh.Messages {
			if !isInjectedUserMessage(msg) {
				continue
			}
			if msg.ID != "" {
				if _, ok := existing[msg.ID]; ok {
					continue
				}
				existing[msg.ID] = struct{}{}
			}
			sess.Messages = append(sess.Messages, msg)
			changed = true
		}
	}
	if fresh.Metadata != nil {
		merged := make(map[string]interface{}, len(fresh.Metadata)+len(sess.Metadata))
		for key, value := range fresh.Metadata {
			merged[key] = value
		}
		// Preserve metadata produced by the in-flight turn (for example token
		// estimates) because it may not have been saved when we reload DB-only
		// injected messages. Approval audit is written by the HTTP server while
		// the agent turn is in flight, so the fresh DB copy must win.
		for key, value := range sess.Metadata {
			if key == "approval_audit" {
				continue
			}
			merged[key] = value
		}
		sess.Metadata = merged
	}
	sess.TaskProgress = fresh.TaskProgress
	if fresh.Status == session.StatusInputRequired || fresh.Status == session.StatusWaitingExternal {
		sess.Status = fresh.Status
	}
	if fresh.UpdatedAt.After(sess.UpdatedAt) {
		sess.UpdatedAt = fresh.UpdatedAt
	}
	return changed
}

func isInjectedUserMessage(msg session.Message) bool {
	if msg.Role != "user" || msg.Metadata == nil {
		return false
	}
	raw, ok := msg.Metadata["injected_during_run"]
	if !ok {
		return false
	}
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

func sessionMessagePrefixMatches(full []session.Message, prefix []session.Message) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i := range prefix {
		if full[i].ID == "" || prefix[i].ID == "" || full[i].ID != prefix[i].ID {
			return false
		}
	}
	return true
}

func hasExternalWaitResult(results []session.ToolResult) bool {
	for _, result := range results {
		if result.Metadata == nil {
			continue
		}
		raw, ok := result.Metadata[toolMetadataExternalWait]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case bool:
			if value {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(value), "true") {
				return true
			}
		}
	}
	return false
}

func (a *Agent) callLLM(ctx context.Context, request *llm.ChatRequest, step int, onEvent func(Event)) (*llm.ChatResponse, error) {
	// When no event sink is provided, use non-streaming Chat.
	// This avoids "partial stream emitted" fallback lock-in and lets fallback chains
	// seamlessly move to the next provider on retryable failures.
	if onEvent == nil {
		return a.llmClient.Chat(ctx, request)
	}

	streamClient, ok := a.llmClient.(llm.StreamingClient)
	if !ok {
		return a.llmClient.Chat(ctx, request)
	}

	return streamClient.ChatStream(ctx, request, func(ev llm.StreamEvent) error {
		if onEvent == nil {
			return nil
		}
		if ev.Type == llm.StreamEventContentDelta && ev.ContentDelta != "" {
			onEvent(Event{
				Type:  EventAssistantDelta,
				Step:  step,
				Delta: ev.ContentDelta,
			})
		}
		if ev.Type == llm.StreamEventProviderTrace {
			onEvent(Event{
				Type: EventProviderTrace,
				Step: step,
				Provider: &ProviderTraceEvent{
					Provider:      ev.Provider,
					Model:         ev.Model,
					Attempt:       ev.Attempt,
					MaxAttempts:   ev.MaxAttempts,
					NodeIndex:     ev.NodeIndex,
					TotalNodes:    ev.TotalNodes,
					Phase:         ev.Phase,
					Reason:        ev.Reason,
					FallbackTo:    ev.FallbackTo,
					FallbackModel: ev.FallbackModel,
					Recovered:     ev.Recovered,
				},
			})
		}
		if isLLMRuntimeForwardEvent(ev) {
			runtime := ev
			onEvent(Event{
				Type:    EventLLMRuntime,
				Step:    step,
				Runtime: &runtime,
			})
		}
		return nil
	})
}

func isLLMRuntimeForwardEvent(ev llm.StreamEvent) bool {
	switch ev.Type {
	case llm.StreamEventReasoningDelta,
		llm.StreamEventToolStarted,
		llm.StreamEventToolUpdated,
		llm.StreamEventToolInputCompleted,
		llm.StreamEventToolCompleted,
		llm.StreamEventToolOutput,
		llm.StreamEventCost,
		llm.StreamEventRuntimeWarning:
		return true
	default:
		return false
	}
}
