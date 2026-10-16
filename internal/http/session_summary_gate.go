package http

import (
	"context"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

const (
	sessionSummaryRefreshIntervalSettingKey = "AAGENT_SESSION_SUMMARY_REFRESH_INTERVAL"
	defaultSessionSummaryRefreshInterval    = 5
	sessionSummaryGateMaxBytes              = 16 * 1024
	sessionSummaryGateQuestion              = "Does this existing summary still accurately describe the session after the latest exchange?"
)

// Only successful LLM summaries are eligible for reuse. History is process-local:
// after a restart, regenerate rather than mistaking a persisted fallback for one.
type sessionSummaryHistory struct {
	summary  string
	template string
	runs     int
}

type sessionSummaryClassifier interface {
	SystemOne(context.Context, jev.SystemOneRequest) (*jev.SystemOneResponse, error)
}

func (s *Server) sessionSummaryRefreshInterval() int {
	if settings, err := s.store.GetSettings(); err == nil {
		if interval, err := strconv.Atoi(strings.TrimSpace(settings[sessionSummaryRefreshIntervalSettingKey])); err == nil && interval > 0 {
			return interval
		}
	}
	return defaultSessionSummaryRefreshInterval
}

func (s *Server) canReuseSessionSummary(ctx context.Context, summary string, sess *session.Session) bool {
	if s.config == nil {
		return false
	}
	client, key := s.systemOneClient()
	if client == nil {
		return false
	}
	secrets, safe := s.jevEgressSecrets(key)
	return safe && sessionSummaryStillAccurate(ctx, client, secrets, summary,
		latestSessionMessageContent(sess.Messages, "user"), latestSessionMessageContent(sess.Messages, "assistant"))
}

func sessionSummaryStillAccurate(ctx context.Context, client sessionSummaryClassifier, secrets []string, summary, user, assistant string) bool {
	if client == nil || strings.TrimSpace(summary) == "" {
		return false
	}
	parts := []string{summary, user, assistant}
	// Check the full inputs before capping, so truncation cannot hide credentials.
	for i, part := range parts {
		if !utf8.ValidString(part) || !jev.SafeEgressText(part, secrets) {
			return false
		}
		parts[i] = llm.HeadTail(part, (sessionSummaryGateMaxBytes-128)/len(parts))
	}
	state := "Existing summary:\n" + parts[0] + "\nLatest user message:\n" + parts[1] + "\nFinal assistant text:\n" + parts[2]
	if !jev.SafeEgressText(state, secrets) {
		return false
	}
	response, err := client.SystemOne(ctx, jev.SystemOneRequest{
		State: state,
		Questions: map[string]jev.Question{
			"accurate": {Type: "noul", Instructions: sessionSummaryGateQuestion},
		},
	})
	if err != nil || response == nil {
		return false
	}
	probability := response.Answers["accurate"].Noul
	return probability != nil && !math.IsNaN(*probability) && *probability >= 0.8 && *probability <= 1
}

// Settings saves and prompt snapshots share a lock so even disable/re-enable
// without a session run invalidates reuse and any in-flight summary completion.
func (s *Server) saveSettingsWithSessionSummaryPrompt(settings, customEnv map[string]string) error {
	s.sessionSummaryMu.Lock()
	defer s.sessionSummaryMu.Unlock()
	previous, err := s.store.GetSettings()
	if err != nil {
		return err
	}
	if customStore, ok := s.store.(storage.CustomEnvStore); ok {
		err = customStore.SaveSettingsAndCustomEnv(settings, customEnv)
	} else {
		err = s.store.SaveSettings(settings)
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(previous[sessionSummaryPromptTemplateSettingKey]) != strings.TrimSpace(settings[sessionSummaryPromptTemplateSettingKey]) {
		clear(s.sessionSummaryLatest)
		for id, history := range s.sessionSummaryHistory {
			// Keep the last successful text as fallback, but not its eligibility for reuse.
			history.template = ""
			s.sessionSummaryHistory[id] = history
		}
	}
	return nil
}
