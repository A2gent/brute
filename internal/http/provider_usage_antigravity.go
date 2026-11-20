package http

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/antigravitycli"
)

const antigravityUsageSource = "Antigravity quota (retrieveUserQuotaSummary)"

func (s *Server) antigravityUsageStatus(ctx context.Context) (ProviderUsageResponse, error) {
	response := ProviderUsageResponse{
		Provider:    string(config.ProviderGoogle),
		Source:      antigravityUsageSource,
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
		Refreshable: true,
	}

	if s.parentProxyAvailable() {
		return s.fetchParentProviderUsage(ctx, config.ProviderGoogle)
	}

	if !antigravitycli.IsAvailable() {
		response.Status = providerUsageStatusNotConfigured
		response.UsageLeftText = "Usage left unavailable - Antigravity CLI is not available. Install it with `" + strings.TrimSpace(antigravitycli.InstallCommand) + "`."
		return response, nil
	}

	accessToken, err := antigravitycli.ResolveAccessToken()
	if err != nil {
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable - " + err.Error()
		return response, nil
	}

	summary, err := antigravitycli.FetchQuotaSummary(ctx, nil, "", accessToken)
	if err != nil {
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable - failed to fetch Antigravity quota. Brute will still surface quota errors when requests fail."
		response.Error = err.Error()
		return response, err
	}

	response.UsageBars = antigravityUsageBars(summary)
	if len(response.UsageBars) == 0 {
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable - Antigravity returned no weekly or 5h quota buckets."
		return response, nil
	}
	response.Status = providerUsageStatusAvailable
	response.UsageLeftText = antigravityUsageSummaryText(response.UsageBars)
	return response, nil
}

func antigravityUsageBars(summary antigravitycli.QuotaSummary) []ProviderUsageBar {
	now := time.Now()
	bars := make([]ProviderUsageBar, 0)
	for _, group := range summary.Groups {
		for _, bucket := range group.Buckets {
			windowLength, windowLabel, ok := antigravityWindow(bucket.Window)
			if !ok {
				continue
			}
			left := int(math.Round(clampPercent(bucket.RemainingFraction * 100)))
			used := 100 - left
			status := "ok"
			if left <= 0 {
				status = "limit_reached"
			}
			bar := ProviderUsageBar{
				Label:         strings.TrimSpace(group.DisplayName + " " + windowLabel),
				UsedPercent:   used,
				LeftPercent:   left,
				Status:        status,
				WindowSeconds: int64(windowLength / time.Second),
				LongTerm:      windowLength >= 24*time.Hour,
			}
			if resetAt, err := time.Parse(time.RFC3339, bucket.ResetTime); err == nil {
				bar.ResetsAt = resetAt.UTC().Format(time.RFC3339)
				bar.ResetText = providerUsageResetInText(resetAt, now)
			}
			bars = append(bars, bar)
		}
	}
	return bars
}

func antigravityWindow(window string) (time.Duration, string, bool) {
	switch strings.ToLower(strings.TrimSpace(window)) {
	case "weekly":
		return 7 * 24 * time.Hour, "weekly", true
	case "5h":
		return 5 * time.Hour, "5h", true
	default:
		return 0, "", false
	}
}

func antigravityUsageSummaryText(bars []ProviderUsageBar) string {
	parts := make([]string, 0, len(bars))
	for _, bar := range bars {
		parts = append(parts, fmt.Sprintf("%s %d%% left", bar.Label, bar.LeftPercent))
	}
	return strings.Join(parts, " · ")
}
