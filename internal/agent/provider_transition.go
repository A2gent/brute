package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/session"
	"github.com/google/uuid"
)

var ErrTransitionPaused = errors.New("provider transition paused")

const transitionContinuationPrompt = "Continue the task from the summary; do not repeat completed tools."

func cloneTransitionSession(sess *session.Session) *session.Session {
	data, _ := json.Marshal(sess)
	var copy session.Session
	_ = json.Unmarshal(data, &copy)
	return &copy
}
func (a *Agent) saveTransition(sess *session.Session, t *session.ProviderTransition, onEvent func(Event)) error {
	session.PutProviderTransition(sess, t)
	if err := a.sessionManager.Save(sess); err != nil {
		return fmt.Errorf("persist transition: %w", err)
	}
	if onEvent != nil {
		snapshot := session.CurrentProviderTransition(sess)
		onEvent(Event{Type: EventProviderTransition, Transition: snapshot})
	}
	return nil
}
func (a *Agent) pauseTransition(sess *session.Session, t *session.ProviderTransition, err error, onEvent func(Event)) error {
	previousPhase := t.Phase
	t.Phase = "paused"
	t.Error = err.Error()
	if previousPhase == "continuing" {
		t.Error = "continuation interrupted: " + t.Error
		t.TargetOutcome = "failed"
	}
	sess.SetStatus(session.StatusPaused)
	if saveErr := a.saveTransition(sess, t, onEvent); saveErr != nil {
		return fmt.Errorf("%w: %v; %v", ErrTransitionPaused, err, saveErr)
	}
	return fmt.Errorf("%w: %v", ErrTransitionPaused, err)
}
func (a *Agent) requestFallbackTransition(sess *session.Session, next *fallback.TransitionRequired, onEvent func(Event)) error {
	if previous := session.CurrentProviderTransition(sess); previous != nil && !previous.Terminal() {
		previous.Phase = "completed"
		previous.TargetOutcome = next.SourceOutcome
		previous.Error = "target did not respond successfully; another transition required"
		if err := a.saveTransition(sess, previous, onEvent); err != nil {
			return err
		}
	}
	t, err := session.NewProviderTransition(uuid.NewString(), session.ProviderEndpoint{Provider: next.Source.Name, Model: next.Source.Model}, session.ProviderEndpoint{Provider: next.Target.Name, Model: next.Target.Model}, "fallback", a.config.TransitionMode)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTransitionPaused, err)
	}
	t.Reason = next.Reason
	t.SourceOutcome = next.SourceOutcome
	t.RetryCount = &next.RetryCount
	t.FallbackIndex = next.Index
	if chain, _ := sess.Metadata["provider"].(string); chain != "" {
		t.ChainProvider = chain
	}
	return a.saveTransition(sess, t, onEvent)
}

// transitionBoundary runs only between complete tool batches, never in a provider
// callback. All mutations activating a new context are committed together.
func (a *Agent) transitionBoundary(ctx context.Context, sess *session.Session, onEvent func(Event)) error {
	t := session.CurrentProviderTransition(sess)
	if t == nil || t.Terminal() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return a.pauseTransition(sess, t, err, onEvent)
	}
	if t.Confirmation == "pending" {
		sess.SetStatus(session.StatusInputRequired)
		if err := a.saveTransition(sess, t, onEvent); err != nil {
			return err
		}
		return ErrTransitionPaused
	}
	if t.Phase == "paused" {
		return fmt.Errorf("%w: %s", ErrTransitionPaused, t.Error)
	}
	if t.Phase == "continuing" && a.activeTransitionID == t.ID {
		return nil
	}
	if t.Phase == "compacting" || t.Phase == "continuing" {
		return a.pauseTransition(sess, t, errors.New(t.Phase+" interrupted; explicit recovery required"), onEvent)
	}
	var target llm.Client
	window := a.config.ContextWindow
	var err error
	if a.config.ResolveTransitionTarget != nil {
		target, window, err = a.config.ResolveTransitionTarget(sess, t.Target)
	} else if chain, ok := a.llmClient.(*fallback.Client); ok && t.Cause == "fallback" {
		if node, exists := chain.NodeAt(t.FallbackIndex); exists && node.Name == t.Target.Provider && node.Model == t.Target.Model {
			target = node.Client
		}
	}
	if target == nil && err == nil {
		err = errors.New("target resolver unavailable")
	}
	if err != nil {
		return a.pauseTransition(sess, t, err, onEvent)
	}
	if t.ContextStrategy == "compact" && (t.Target.Provider == "cursor" || t.Target.Provider == "kimi_cli") {
		return a.pauseTransition(sess, t, errors.New("target CLI cannot guarantee tool-free summarization; use switch_auto or a tool-free target"), onEvent)
	}
	t.Compaction.TargetWindow = window
	if t.Phase != "ready" {
		if t.ContextStrategy == "compact" {
			t.Phase = "compacting"
			startedAt := time.Now().UTC()
			t.Compaction.StartedAt = &startedAt
			if err := a.saveTransition(sess, t, onEvent); err != nil {
				return err
			}
		}
		candidate := cloneTransitionSession(sess)
		if t.ContextStrategy == "compact" {
			summary, usage, before, err := a.summarizeTransition(ctx, sess, target, t.Target.Model, window, &t.Compaction)
			if t.Compaction.StartedAt != nil {
				t.Compaction.DurationMs = time.Since(*t.Compaction.StartedAt).Milliseconds()
			}
			t.Compaction.Summarizer = &t.Target
			t.Compaction.Usage = &usage
			t.Compaction.BeforeEstimate = before
			if err != nil {
				t.Compaction.Outcome = "failed"
				return a.pauseTransition(sess, t, err, onEvent)
			}
			a.activateTransitionSummary(candidate, summary, t.ID)
			// The summary is produced by the concrete target, never the source.
			msg := &candidate.Messages[len(sess.Messages)]
			msg.Provenance = llm.NewProvenance(t.Target.Provider, t.Target.Model)
			msg.Metadata["llm_provenance"] = map[string]interface{}{"provider": t.Target.Provider, "model": t.Target.Model}
			a.addTokenUsageMetadata(candidate, usage)
			t.Compaction.Outcome = "succeeded"
		}
		invalidateTransitionState(candidate)
		candidate.Metadata["provider_context_epoch"] = uuid.NewSHA1(uuid.NameSpaceOID, []byte(candidate.ID+":"+t.ID)).String()
		// Estimate the rebuilt request, not the old provider's reported token count.
		preview := *a
		preview.config.Model = t.Target.Model
		preview.config.UsePreviousResponse = false
		preview.config.UseProviderSession = false
		request := preview.buildRequest(candidate)
		t.Compaction.AfterEstimate = transitionRequestEstimate(request)
		if window > 0 && t.Compaction.AfterEstimate+1024 > window {
			return a.pauseTransition(sess, t, errors.New("rebuilt context exceeds target window; history preserved"), onEvent)
		}
		if err := ctx.Err(); err != nil {
			return a.pauseTransition(sess, t, err, onEvent)
		}
		if t.Mode == "linked_session" {
			return a.linkTransition(sess, candidate, t, onEvent)
		}
		if t.Cause == "fallback" {
			candidate.Metadata["fallback_active_provider"] = t.Target.Provider
			candidate.Metadata["fallback_active_model"] = t.Target.Model
			progress, _ := candidate.Metadata["fallback_progress"].(map[string]interface{})
			if progress == nil {
				progress = map[string]interface{}{}
			}
			progress[t.ChainProvider] = t.FallbackIndex
			candidate.Metadata["fallback_progress"] = progress
		} else {
			candidate.Metadata["provider"] = t.Target.Provider
			candidate.Metadata["model"] = t.Target.Model
		}
		candidate.Metadata["context_window"] = window
		t.Phase = "ready"
		t.TargetOutcome = "selected"
		if err := a.saveTransition(candidate, t, onEvent); err != nil {
			return a.pauseTransition(sess, t, err, onEvent)
		}
		*sess = *candidate
	}
	if chain, ok := a.llmClient.(*fallback.Client); ok && t.Cause == "fallback" {
		chain.CommitTransition(t.FallbackIndex)
	} else {
		a.llmClient = target
	}
	a.config.Provider = t.Target.Provider
	a.config.Model = t.Target.Model
	a.config.ContextWindow = window
	a.config.UsePreviousResponse = false
	a.config.UseProviderSession = false
	a.config.ReasoningEffort = ""
	t.Phase = "continuing"
	if err := a.saveTransition(sess, t, onEvent); err != nil {
		return err
	}
	a.activeTransitionID = t.ID
	return nil
}
func (a *Agent) completeTransition(sess *session.Session, onEvent func(Event)) error {
	return a.finishTransition(sess, "succeeded", onEvent)
}

// finishTransition closes a continuing transition with the given target outcome,
// so step-limit finalization does not leave a stale "continuing" state behind.
func (a *Agent) finishTransition(sess *session.Session, outcome string, onEvent func(Event)) error {
	t := session.CurrentProviderTransition(sess)
	if t == nil || t.Phase != "continuing" {
		return nil
	}
	t.Phase = "completed"
	t.TargetOutcome = outcome
	return a.saveTransition(sess, t, onEvent)
}
func invalidateTransitionState(sess *session.Session) {
	for _, key := range []string{"last_response_id", "provider_session_cursor", "provider_session_identity", "last_llm_request_at", "last_llm_provider", "last_llm_model", "prompt_cache_key", "routed_provider", "routed_model", "fallback_active_provider", "fallback_active_model"} {
		sess.Metadata[key] = ""
	}
	sess.Metadata["current_context_tokens"] = 0
}
func (a *Agent) activateTransitionSummary(sess *session.Session, summary, id string) {
	// Keep every original message in order. Pending user prompts are copied to the
	// active context with new IDs; their originals remain in the archived prefix.
	end := len(sess.Messages)
	start := end
	for start > 0 && sess.Messages[start-1].Role == "user" {
		start--
	}
	pending := append([]session.Message(nil), sess.Messages[start:]...)
	sess.AddAssistantMessageWithMetadata(summary, nil, map[string]interface{}{messageMetadataCompaction: true, "provider_transition_id": id})
	for _, msg := range pending {
		msg.ID = ""
		// Fresh timestamp keeps the copy after the summary when history is reloaded in time order.
		msg.Timestamp = time.Time{}
		sess.AddMessage(msg)
	}
	if len(pending) == 0 {
		sess.AddUserMessage(transitionContinuationPrompt)
	}
}
func (a *Agent) linkTransition(source, candidate *session.Session, t *session.ProviderTransition, onEvent func(Event)) error {
	// Deterministic ID makes a crash between child-save and source-save recoverable.
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(source.ID+":"+t.ID)).String()
	linked, err := a.sessionManager.Get(id)
	if err != nil {
		linked = session.NewWithStatus(source.AgentID, session.StatusQueued)
		linked.ID = id
		linked.ProjectID = source.ProjectID
		linked.Title = source.Title
		linked.Metadata = map[string]interface{}{"provider": t.Target.Provider, "model": t.Target.Model, "link_type": "continuation", "continuation_source_id": source.ID, "provider_transition_id": t.ID}
		for _, key := range []string{"work_dir", "system_prompt_snapshot"} {
			if value, ok := source.Metadata[key]; ok {
				linked.Metadata[key] = value
			}
		}
		linked.Messages = a.getActiveConversationMessages(candidate)
		for i := range linked.Messages {
			linked.Messages[i].ID = uuid.NewString()
		}
		if err := a.sessionManager.Save(linked); err != nil {
			return a.pauseTransition(source, t, err, onEvent)
		}
	}
	t.LinkedSessionID = id
	t.Phase = "linked"
	source.SetStatus(session.StatusPaused)
	if err := a.saveTransition(source, t, onEvent); err != nil {
		return err
	}
	return fmt.Errorf("%w: linked continuation %s is queued", ErrTransitionPaused, id)
}

// Conservative byte estimates deliberately overestimate text. Images are charged
// separately; this is admission safety, not billing or a provider tokenizer.
func transitionRequestEstimate(req *llm.ChatRequest) int {
	raw, _ := json.Marshal(req.Messages)
	defs, _ := json.Marshal(req.Tools)
	return len(raw) + len(defs) + len(req.SystemPrompt) + 128
}

// summarizeTransition records Jev history filtering into stats; before stays the
// unfiltered history size so the notice reflects the real reduction.
func (a *Agent) summarizeTransition(ctx context.Context, sess *session.Session, client llm.Client, model string, window int, stats *session.TransitionCompaction) (string, llm.TokenUsage, int, error) {
	messages := a.getActiveConversationMessages(sess)
	for len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		messages = messages[:len(messages)-1]
	}
	raw, err := json.Marshal(messages)
	usage := llm.TokenUsage{}
	if err != nil {
		return "", usage, 0, err
	}
	before := len(raw)
	if before > 16*1024*1024 {
		return "", usage, before, errors.New("history exceeds bounded summarization budget")
	}
	// A retry after a pause must not show the previous attempt's Jev result.
	stats.JevUsed, stats.JevScored, stats.JevDropped, stats.JevDroppedBytes = false, 0, 0, 0
	if len(messages) == 0 {
		return "No prior completed work. Follow the pending user prompt.", usage, before, nil
	}
	// The pending user prompt is still in sess, so it anchors the relevance task.
	if filtered, jevStats := filterHistoryWithJev(ctx, a.config.RelevanceGate, compactionRelevanceTask(sess), messages); jevStats.Used {
		stats.JevUsed, stats.JevScored, stats.JevDropped, stats.JevDroppedBytes = true, jevStats.Scored, jevStats.Dropped, jevStats.DroppedBytes
		if raw, err = json.Marshal(filtered); err != nil {
			return "", usage, before, err
		}
	}
	if window <= 0 {
		window = 8192
	}
	budget := window - len(defaultCompactionPrompt) - 1536
	if budget < 256 {
		return "", usage, before, errors.New("target context window too small for summarization")
	}
	text := string(raw)
	// Bounded map/reduce uses the target directly, never the failing source or a
	// fallback wrapper. No tools, cursors or session ID can leak into summary calls.
	for round := 0; round < 8; round++ {
		var summaries []string
		for len(text) > 0 {
			size := len(text)
			if size > budget {
				size = budget
				for size > 0 && (text[size]&0xc0) == 0x80 {
					size--
				}
			}
			chunk := text[:size]
			text = text[size:]
			if err := ctx.Err(); err != nil {
				return "", usage, before, err
			}
			request := &llm.ChatRequest{Model: model, SystemPrompt: defaultCompactionPrompt, Messages: []llm.Message{{Role: "user", Content: chunk}}, MaxTokens: 1024, Temperature: 0.2}
			var response *llm.ChatResponse
			var err error
			if stream, ok := client.(llm.StreamingClient); ok {
				response, err = stream.ChatStream(summaryContext{ctx}, request, func(llm.StreamEvent) error { return nil })
			} else {
				response, err = client.Chat(summaryContext{ctx}, request)
			}
			if err != nil {
				return "", usage, before, err
			}
			if response == nil {
				return "", usage, before, errors.New("nil compaction response")
			}
			usage.InputTokens += response.Usage.InputTokens
			usage.OutputTokens += response.Usage.OutputTokens
			usage.CachedInputTokens += response.Usage.CachedInputTokens
			usage.ReasoningTokens += response.Usage.ReasoningTokens
			if err := ctx.Err(); err != nil {
				return "", usage, before, err
			}
			summary := strings.TrimSpace(response.Content)
			if summary == "" || len(response.ToolCalls) > 0 {
				return "", usage, before, errors.New("empty or tool-producing compaction response")
			}
			summaries = append(summaries, summary)
		}
		text = strings.Join(summaries, "\n")
		if len(summaries) == 1 && len(text) <= budget {
			return text, usage, before, nil
		}
	}
	return "", usage, before, errors.New("summary reduction exceeded safe limit")
}

// Preserve cancellation/deadlines but hide session/tool-bridge values. Some CLI
// clients derive MCP access from context rather than the request's tool list.
type summaryContext struct{ context.Context }

func (summaryContext) Value(any) any { return nil }
