package http

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/go-chi/chi/v5"
)

const (
	providerUsageStatusAvailable     = "available"
	providerUsageStatusUnavailable   = "unavailable"
	providerUsageStatusUnsupported   = "unsupported"
	providerUsageStatusNotConfigured = "not_configured"

	anthropicUsageSource               = "Claude Code statusLine cache"
	defaultClaudeRateLimitsCachePath   = "~/.a2gent/claude-rate-limits.json"
	containerClaudeRateLimitsCachePath = "/data/a2gent-parent-claude-rate-limits.json"
	claudeRateLimitsCachePathEnv       = "AAGENT_CLAUDE_RATE_LIMITS_PATH"
	claudeRateLimitsCacheMaxAgeEnv     = "AAGENT_CLAUDE_RATE_LIMITS_MAX_AGE"
	defaultClaudeRateLimitsCacheMaxAge = 12 * time.Hour
)

type claudeRateLimitCache struct {
	RateLimits claudeRateLimitWindows `json:"rate_limits"`
}

type claudeRateLimitWindows struct {
	FiveHour *claudeRateLimitWindow `json:"five_hour"`
	SevenDay *claudeRateLimitWindow `json:"seven_day"`
}

type claudeRateLimitWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

func (s *Server) handleProviderUsage(w http.ResponseWriter, r *http.Request) {
	providerRef := config.NormalizeProviderRef(chi.URLParam(r, "providerType"))
	if config.GetProviderDefinitionForRef(providerRef) == nil {
		s.errorResponse(w, http.StatusNotFound, "Unknown provider")
		return
	}

	usage := s.providerUsageStatus(r.Context(), config.ProviderType(providerRef))
	if !s.parentProxyAvailable() {
		s.usageHistory.record(usage, time.Now())
	}
	s.jsonResponse(w, http.StatusOK, usage)
}

func (s *Server) providerUsageStatus(ctx context.Context, providerType config.ProviderType) ProviderUsageResponse {
	response := ProviderUsageResponse{
		Provider:    string(providerType),
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
		Refreshable: true,
	}

	if config.IsClaudeProviderRef(string(providerType)) {
		return s.anthropicUsageStatus(providerType)
	}

	switch providerType {
	case config.ProviderOpenAI:
		response.Source = "OpenAI API"
		if !s.providerConfiguredForUse(providerType) {
			response.Status = providerUsageStatusNotConfigured
			response.UsageLeftText = "Usage left unavailable — configure an OpenAI API key first."
			return response
		}
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable — OpenAI does not expose remaining quota for this API key. Check the OpenAI dashboard; Brute will surface quota or billing errors when requests fail."
		return response
	case config.ProviderOpenAICodex:
		usage, err := s.openAICodexUsageStatus(ctx)
		if err != nil {
			response.Source = "OpenAI Codex OAuth (ChatGPT usage)"
			response.Status = providerUsageStatusUnavailable
			response.UsageLeftText = "Usage left unavailable — failed to fetch ChatGPT Codex usage from /backend-api/wham/usage. Brute will still surface quota or plan errors when requests fail."
			response.Error = err.Error()
			return response
		}
		return usage
	case config.ProviderCursor:
		usage, err := s.cursorUsageStatus(ctx)
		if err != nil {
			return usage
		}
		return usage
	case config.ProviderOpenRouter:
		return s.openRouterUsageStatus(ctx)
	default:
		response.Status = providerUsageStatusUnsupported
		response.Source = strings.TrimSpace(string(providerType))
		response.UsageLeftText = "Usage left is not supported for this provider yet."
		response.Refreshable = false
		return response
	}
}

func (s *Server) anthropicUsageStatus(providerType config.ProviderType) ProviderUsageResponse {
	response := ProviderUsageResponse{
		Provider:    string(providerType),
		Source:      anthropicUsageSource,
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
		Refreshable: true,
	}

	if !s.providerConfiguredForUse(providerType) {
		response.Status = providerUsageStatusNotConfigured
		response.UsageLeftText = "Usage left unavailable — Claude Code CLI is not available. Install Claude Code or set AAGENT_CLAUDE_CLI_PATH."
		return response
	}

	status, err := readClaudeRateLimitCache(time.Now())
	if err != nil {
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = err.Error()
		return response
	}
	status.Provider = string(providerType)
	return status
}

func providerUsageLimitReached(usage ProviderUsageResponse, now time.Time) (bool, string) {
	if usage.Status != providerUsageStatusAvailable {
		return false, ""
	}
	for _, bar := range usage.UsageBars {
		if bar.Status == "limit_reached" || bar.LeftPercent <= 0 {
			if providerUsageResetElapsed(bar, now) {
				continue
			}
			detail := strings.TrimSpace(bar.Label)
			if detail == "" {
				detail = strings.TrimSpace(usage.UsageLeftText)
			}
			if reset := strings.TrimSpace(bar.ResetText); reset != "" {
				if detail != "" {
					detail += " "
				}
				detail += reset
			}
			return true, detail
		}
	}
	return false, ""
}

// providerUsageBelowMinBalance reports whether a long-term (weekly) bar has less than
// minLeftPercent left, so sessions stop using the provider before it is fully drained.
// WHY: short windows (5h) reset quickly and are already handled by the hard limit check.
func providerUsageBelowMinBalance(usage ProviderUsageResponse, minLeftPercent int, now time.Time) (bool, string) {
	if minLeftPercent <= 0 || usage.Status != providerUsageStatusAvailable {
		return false, ""
	}
	for _, bar := range usage.UsageBars {
		if !bar.LongTerm || bar.LeftPercent >= minLeftPercent || providerUsageResetElapsed(bar, now) {
			continue
		}
		detail := fmt.Sprintf("%s %d%% left, below %d%% minimum", strings.TrimSpace(bar.Label), bar.LeftPercent, minLeftPercent)
		return true, strings.TrimSpace(detail)
	}
	return false, ""
}

func providerUsageResetElapsed(bar ProviderUsageBar, now time.Time) bool {
	if raw := strings.TrimSpace(bar.ResetsAt); raw != "" {
		resetAt, err := time.Parse(time.RFC3339, raw)
		if err == nil {
			return !resetAt.After(now)
		}
	}
	// Backward compatibility for callers/tests that only set reset_text with an RFC3339 timestamp.
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(bar.ResetText), "resets "))
	if raw == "" {
		return false
	}
	resetAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	return !resetAt.After(now)
}

func providerUsageResetInText(resetAt time.Time, now time.Time) string {
	remaining := resetAt.Sub(now)
	if remaining <= 0 {
		return ""
	}
	return "resets in " + formatProviderUsageCountdown(remaining)
}

func formatProviderUsageCountdown(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "less than 1min"
	}
	days := d / (24 * time.Hour)
	d -= days * (24 * time.Hour)
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute

	parts := make([]string, 0, 3)
	if days > 0 {
		if days == 1 {
			parts = append(parts, "1 day")
		} else {
			parts = append(parts, fmt.Sprintf("%d days", days))
		}
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dmin", minutes))
	}
	return strings.Join(parts, " ")
}

func readClaudeRateLimitCache(now time.Time) (ProviderUsageResponse, error) {
	cachePath, err := claudeRateLimitsCachePath()
	if err != nil {
		return ProviderUsageResponse{}, err
	}
	maxAge, err := claudeRateLimitsCacheMaxAge()
	if err != nil {
		return ProviderUsageResponse{}, err
	}
	info, err := os.Stat(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return ProviderUsageResponse{}, fmt.Errorf("Usage left unavailable — Claude Code statusLine cache not found at %s. Enable Claude usage in the provider card, configure a statusLine script to write rate_limits JSON there, or set %s.", cachePath, claudeRateLimitsCachePathEnv)
		}
		return ProviderUsageResponse{}, fmt.Errorf("Usage left unavailable — failed to read Claude Code statusLine cache at %s: %w", cachePath, err)
	}
	age := now.Sub(info.ModTime())
	if age < 0 {
		age = 0
	}
	if age > maxAge {
		return ProviderUsageResponse{}, fmt.Errorf("Usage left unavailable — Claude Code statusLine cache at %s is stale (%s old; max %s). Refresh Claude Code so the statusLine script writes a newer snapshot.", cachePath, formatDurationApprox(age), formatDurationApprox(maxAge))
	}

	body, err := os.ReadFile(cachePath)
	if err != nil {
		return ProviderUsageResponse{}, fmt.Errorf("Usage left unavailable — failed to read Claude Code statusLine cache at %s: %w", cachePath, err)
	}
	var payload claudeRateLimitCache
	if err := json.Unmarshal(body, &payload); err != nil {
		return ProviderUsageResponse{}, fmt.Errorf("Usage left unavailable — failed to parse Claude Code statusLine cache at %s: %w", cachePath, err)
	}

	usageBars := claudeRateLimitUsageBars(payload, now)
	if len(usageBars) == 0 {
		return ProviderUsageResponse{}, fmt.Errorf("Usage left unavailable — Claude Code statusLine cache at %s does not contain rate_limits.five_hour or rate_limits.seven_day yet. Claude Code only provides these fields for Claude.ai subscriber sessions after the first API response.", cachePath)
	}

	return ProviderUsageResponse{
		Provider:      string(config.ProviderAnthropic),
		Status:        providerUsageStatusAvailable,
		UsageLeftText: formatClaudeRateLimitUsage(payload, now),
		UsageBars:     usageBars,
		Source:        anthropicUsageSource,
		CheckedAt:     info.ModTime().UTC().Format(time.RFC3339),
		Refreshable:   true,
	}, nil
}

func claudeRateLimitsCachePath() (string, error) {
	raw := strings.TrimSpace(os.Getenv(claudeRateLimitsCachePathEnv))
	if raw == "" {
		raw = defaultClaudeRateLimitsCachePath
	}
	resolved := expandTilde(raw)
	if strings.TrimSpace(resolved) == "" {
		return "", fmt.Errorf("Usage left unavailable — Claude Code statusLine cache path is empty")
	}
	if abs, err := filepath.Abs(resolved); err == nil {
		return abs, nil
	}
	return resolved, nil
}

func claudeRateLimitsCacheMaxAge() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(claudeRateLimitsCacheMaxAgeEnv))
	if raw == "" {
		return defaultClaudeRateLimitsCacheMaxAge, nil
	}
	age, err := time.ParseDuration(raw)
	if err != nil || age <= 0 {
		return 0, fmt.Errorf("Usage left unavailable — invalid %s value %q; expected a positive Go duration such as 6h or 24h", claudeRateLimitsCacheMaxAgeEnv, raw)
	}
	return age, nil
}

func claudeRateLimitUsageBars(payload claudeRateLimitCache, now time.Time) []ProviderUsageBar {
	bars := make([]ProviderUsageBar, 0, 2)
	if payload.RateLimits.FiveHour != nil {
		bars = append(bars, claudeRateLimitUsageBar("Claude 5h", payload.RateLimits.FiveHour, 5*time.Hour, now))
	}
	if payload.RateLimits.SevenDay != nil {
		bars = append(bars, claudeRateLimitUsageBar("Claude weekly", payload.RateLimits.SevenDay, 7*24*time.Hour, now))
	}
	return bars
}

func claudeRateLimitUsageBar(label string, window *claudeRateLimitWindow, windowLength time.Duration, now time.Time) ProviderUsageBar {
	used := int(math.Round(clampPercent(window.UsedPercentage)))
	left := 100 - used
	if left < 0 {
		left = 0
	}
	status := "ok"
	if left <= 0 {
		status = "limit_reached"
	}
	bar := ProviderUsageBar{
		Label:         label,
		UsedPercent:   used,
		LeftPercent:   left,
		ResetText:     claudeRateLimitResetText(window, now),
		Status:        status,
		WindowSeconds: int64(windowLength / time.Second),
		LongTerm:      windowLength >= 24*time.Hour,
	}
	if window.ResetsAt > 0 {
		bar.ResetsAt = time.Unix(window.ResetsAt, 0).UTC().Format(time.RFC3339)
	}
	return bar
}

func claudeRateLimitResetText(window *claudeRateLimitWindow, now time.Time) string {
	if window == nil || window.ResetsAt <= 0 {
		return ""
	}
	return providerUsageResetInText(time.Unix(window.ResetsAt, 0).UTC(), now)
}

func formatClaudeRateLimitUsage(payload claudeRateLimitCache, now time.Time) string {
	parts := make([]string, 0, 2)
	if payload.RateLimits.FiveHour != nil {
		parts = append(parts, "5h "+formatClaudeRateLimitWindow(payload.RateLimits.FiveHour, now))
	}
	if payload.RateLimits.SevenDay != nil {
		parts = append(parts, "weekly "+formatClaudeRateLimitWindow(payload.RateLimits.SevenDay, now))
	}
	if len(parts) == 0 {
		return "Claude Code statusLine cache is present but does not contain rate-limit windows yet."
	}
	return strings.Join(parts, " · ")
}

func formatClaudeRateLimitWindow(window *claudeRateLimitWindow, now time.Time) string {
	used := clampPercent(window.UsedPercentage)
	left := int(math.Max(0, math.Round(100-used)))
	parts := []string{fmt.Sprintf("%d%% left", left)}
	if reset := claudeRateLimitResetText(window, now); reset != "" {
		parts = append(parts, reset)
	}
	return strings.Join(parts, ", ")
}

func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
