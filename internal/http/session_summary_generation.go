package http

import (
	"context"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

const sessionSummaryTimeout = 20 * time.Second

// refreshSessionSummaryAsync updates the session summary without delaying the
// caller. The cheap deterministic fallback is persisted synchronously; the
// optional LLM summary pass runs in a goroutine on a snapshot because the run
// context is cancelled when the handler returns and the session may be resumed
// by a newer run meanwhile. Only the summary column is written back.
func (s *Server) refreshSessionSummaryAsync(sess *session.Session) {
	if s == nil || sess == nil || (sess.Status != session.StatusCompleted && sess.Status != session.StatusFailed) {
		return
	}

	// WHY: keep a deterministic fallback so summary generation never blocks saving
	// the actual session result, even when the configured LLM/provider is unavailable.
	snapshot := *sess
	snapshot.Messages = append([]session.Message(nil), sess.Messages...)
	snapshot.Metadata = make(map[string]interface{}, len(sess.Metadata))
	for key, value := range sess.Metadata {
		snapshot.Metadata[key] = value
	}
	snapshot.RefreshSummary()
	fallbackSummary := strings.TrimSpace(snapshot.Summary)
	prompt := s.renderSessionSummaryPrompt(&snapshot)
	token, history := s.beginSessionSummary(sess.ID)
	if strings.TrimSpace(prompt) == "" {
		s.preserveSessionSummary(sess, token, fallbackSummary)
		s.finishSessionSummary(sess.ID, token, "", false)
		return
	}
	// Keep a trusted LLM summary while checking it; a skip must not leave the
	// deterministic fallback in its place. Never mutate the live session async.
	currentSummary := fallbackSummary
	if history.summary != "" {
		currentSummary = history.summary
	}
	s.preserveSessionSummary(sess, token, currentSummary)
	interval := s.sessionSummaryRefreshInterval()
	s.sessionSummaryWG.Add(1)
	go func() {
		defer s.sessionSummaryWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), sessionSummaryTimeout)
		defer cancel()
		if history.summary != "" && history.runs < interval && s.canReuseSessionSummary(ctx, history.summary, &snapshot) {
			s.finishSessionSummary(snapshot.ID, token, "", true)
			return
		}
		// Give regeneration its own budget even if the fail-open gate timed out.
		summaryCtx, summaryCancel := context.WithTimeout(context.Background(), sessionSummaryTimeout)
		defer summaryCancel()
		summary := s.generateSessionSummary(summaryCtx, &snapshot, prompt)
		s.finishSessionSummary(snapshot.ID, token, summary, false)
	}()
}

func (s *Server) beginSessionSummary(sessionID string) (uint64, sessionSummaryHistory) {
	s.sessionSummaryMu.Lock()
	defer s.sessionSummaryMu.Unlock()
	if s.sessionSummaryLatest == nil {
		s.sessionSummaryLatest = make(map[string]uint64)
	}
	if s.sessionSummaryHistory == nil {
		s.sessionSummaryHistory = make(map[string]sessionSummaryHistory)
	}
	history := s.sessionSummaryHistory[sessionID]
	history.runs++
	s.sessionSummaryHistory[sessionID] = history
	s.sessionSummarySeq++
	s.sessionSummaryLatest[sessionID] = s.sessionSummarySeq
	return s.sessionSummarySeq, history
}

func (s *Server) preserveSessionSummary(sess *session.Session, token uint64, summary string) {
	s.sessionSummaryMu.Lock()
	defer s.sessionSummaryMu.Unlock()
	if s.sessionSummaryLatest[sess.ID] != token || summary == "" || summary == sess.Summary {
		return
	}
	sess.SetSummary(summary)
	s.persistSessionSummary(sess.ID, summary)
}

// finishSessionSummary releases only the latest pass and commits its result
// under the same lock, so a newer run cannot race with a stale summary write.
func (s *Server) finishSessionSummary(sessionID string, token uint64, summary string, skipped bool) bool {
	s.sessionSummaryMu.Lock()
	defer s.sessionSummaryMu.Unlock()
	if s.sessionSummaryLatest[sessionID] != token {
		return false
	}
	delete(s.sessionSummaryLatest, sessionID)
	if skipped {
		s.sessionSummarySkipCount++
		logging.Info("Session summary LLM regeneration skipped: session=%s skip_count=%d", sessionID, s.sessionSummarySkipCount)
	} else if summary != "" && s.persistSessionSummary(sessionID, summary) {
		s.sessionSummaryHistory[sessionID] = sessionSummaryHistory{summary: summary}
	}
	if s.sessionSummaryHistory[sessionID].summary == "" {
		delete(s.sessionSummaryHistory, sessionID)
	}
	return true
}

// persistSessionSummary writes only the summary column; the session manager
// announces the change to catalog subscribers.
func (s *Server) persistSessionSummary(sessionID, summary string) bool {
	if err := s.sessionManager.SetSessionSummary(sessionID, summary); err != nil {
		logging.Warn("Failed to save session summary: %v", err)
		return false
	}
	return true
}

// generateSessionSummary runs the LLM summary pass. Returns "" on failure so the
// previously persisted LLM summary or deterministic fallback stays in place.
func (s *Server) generateSessionSummary(summaryCtx context.Context, sess *session.Session, prompt string) string {
	settings, settingsErr := s.store.GetSettings()
	if settingsErr != nil {
		logging.Warn("Failed to load settings for session summary provider: %v", settingsErr)
		settings = map[string]string{}
	}
	sessionProviderType := s.resolveSessionProviderType(sess)
	sessionModel := s.resolveSessionModel(sess, sessionProviderType)
	targetConfig := s.resolvePromptLLMTargetWithFallback(settings, promptLLMCaseSessionSummary, sessionProviderType, sessionModel)
	target, err := s.resolveExecutionTarget(summaryCtx, targetConfig.ProviderType, targetConfig.Model, prompt, sess)
	if err != nil {
		logging.Warn("Failed to resolve provider for session summary: %v", err)
		return ""
	}

	effort := auxiliaryReasoningEffort(target.ProviderType, target.Model)
	maxTokens := 80
	if effort != "" && effort != "none" {
		// max_output_tokens includes reasoning. Even minimal effort can exhaust
		// 80 tokens before any text; SummaryFromContent still limits the result.
		maxTokens = 2048
	}
	resp, err := target.Client.Chat(summaryCtx, &llm.ChatRequest{
		Model:           target.Model,
		ReasoningEffort: effort,
		ServiceTier:     auxiliaryBackgroundServiceTier(target.ProviderType),
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
		MaxTokens:   maxTokens,
	})
	if err != nil {
		logging.Warn("Failed to generate session summary: %v", err)
		return ""
	}
	return session.SummaryFromContent(resp.Content)
}

func (s *Server) renderSessionSummaryPrompt(sess *session.Session) string {
	if s == nil || s.store == nil || sess == nil {
		return ""
	}
	settings, err := s.store.GetSettings()
	if err != nil {
		logging.Warn("Failed to load settings for session summary prompt: %v", err)
		return ""
	}
	// Only use the extra LLM summarization pass when the user explicitly saved a
	// summary prompt. Default templates are still exposed to Settings UI, but this
	// avoids surprising extra provider calls on existing installs and tests.
	template := strings.TrimSpace(settings[sessionSummaryPromptTemplateSettingKey])
	if template == "" {
		return ""
	}
	return renderPromptTemplate(template, map[string]string{
		"status":                   string(sess.Status),
		"title":                    sess.Title,
		"initial_user_message":     firstSessionMessageContent(sess.Messages, "user"),
		"latest_assistant_message": latestSessionMessageContent(sess.Messages, "assistant"),
		"transcript":               compactSessionTranscript(sess.Messages, 10, 4000),
	})
}

func firstSessionMessageContent(messages []session.Message, role string) string {
	for _, msg := range messages {
		if msg.Role == role && strings.TrimSpace(msg.Content) != "" {
			return strings.TrimSpace(msg.Content)
		}
	}
	return ""
}

func latestSessionMessageContent(messages []session.Message, role string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == role && strings.TrimSpace(msg.Content) != "" {
			return strings.TrimSpace(msg.Content)
		}
	}
	return ""
}

func compactSessionTranscript(messages []session.Message, maxMessages int, maxRunes int) string {
	if maxMessages <= 0 || maxRunes <= 0 || len(messages) == 0 {
		return ""
	}
	start := len(messages) - maxMessages
	if start < 0 {
		start = 0
	}
	parts := make([]string, 0, len(messages)-start)
	for _, msg := range messages[start:] {
		content := strings.Join(strings.Fields(strings.TrimSpace(msg.Content)), " ")
		if content == "" {
			continue
		}
		parts = append(parts, msg.Role+": "+content)
	}
	text := strings.Join(parts, "\n")
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "…"
}
