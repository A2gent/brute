package http

import (
	"context"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

func (s *Server) refreshSessionSummaryWithPrompt(ctx context.Context, sess *session.Session) {
	if s == nil || sess == nil || (sess.Status != session.StatusCompleted && sess.Status != session.StatusFailed) {
		return
	}

	// WHY: keep a deterministic fallback so summary generation never blocks saving
	// the actual session result, even when the configured LLM/provider is unavailable.
	fallbackSession := *sess
	fallbackSession.RefreshSummary()
	fallbackSummary := strings.TrimSpace(fallbackSession.Summary)

	prompt := s.renderSessionSummaryPrompt(sess)
	if strings.TrimSpace(prompt) == "" {
		if fallbackSummary != "" {
			sess.SetSummary(fallbackSummary)
			_ = s.sessionManager.Save(sess)
		}
		return
	}

	summaryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	providerType := s.resolveSessionProviderType(sess)
	model := s.resolveSessionModel(sess, providerType)
	target, err := s.resolveExecutionTarget(summaryCtx, providerType, model, prompt, sess)
	if err != nil {
		logging.Warn("Failed to resolve provider for session summary: %v", err)
		if fallbackSummary != "" {
			sess.SetSummary(fallbackSummary)
			_ = s.sessionManager.Save(sess)
		}
		return
	}

	resp, err := target.Client.Chat(summaryCtx, &llm.ChatRequest{
		Model: target.Model,
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
		MaxTokens:   80,
	})
	if err != nil {
		logging.Warn("Failed to generate session summary: %v", err)
		if fallbackSummary != "" {
			sess.SetSummary(fallbackSummary)
			_ = s.sessionManager.Save(sess)
		}
		return
	}

	summary := session.SummaryFromContent(resp.Content)
	if summary == "" {
		summary = fallbackSummary
	}
	if summary == "" {
		return
	}
	sess.SetSummary(summary)
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to save generated session summary: %v", err)
	}
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
