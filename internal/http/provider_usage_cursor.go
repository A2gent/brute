package http

import (
	"context"
	"math"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/cursorcli"
)

const cursorUsageSource = "Cursor dashboard (GetCurrentPeriodUsage)"

func (s *Server) cursorUsageStatus(ctx context.Context) (ProviderUsageResponse, error) {
	response := ProviderUsageResponse{
		Provider:    string(config.ProviderCursor),
		Source:      cursorUsageSource,
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
		Refreshable: true,
	}

	if !s.providerConfiguredForUse(config.ProviderCursor) {
		response.Status = providerUsageStatusNotConfigured
		response.UsageLeftText = "Usage left unavailable — Cursor Agent CLI is not available. Install Cursor CLI with `curl https://cursor.com/install -fsS | bash` or set AAGENT_CURSOR_CLI_PATH."
		return response, nil
	}

	accessToken, err := cursorcli.ResolveAccessToken()
	if err != nil {
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable — " + err.Error()
		return response, nil
	}

	usage, err := cursorcli.FetchPeriodUsage(ctx, nil, "", accessToken)
	if err != nil {
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable — failed to fetch Cursor plan usage from GetCurrentPeriodUsage. Brute will still surface quota or billing errors when requests fail."
		response.Error = err.Error()
		return response, err
	}

	response.Status = providerUsageStatusAvailable
	response.UsageLeftText = cursorcli.FormatPeriodUsageSummary(usage)
	response.UsageBars = cursorUsageBars(usage)
	return response, nil
}

func cursorUsageBars(usage cursorcli.PeriodUsage) []ProviderUsageBar {
	resetText := cursorBillingCycleResetText(usage.BillingCycleEnd)
	return []ProviderUsageBar{
		withCursorResetText(cursorUsageBar("Cursor total", usage.PlanUsage.TotalPercentUsed), resetText),
		withCursorResetText(cursorUsageBar("Cursor Auto", usage.PlanUsage.AutoPercentUsed), resetText),
		withCursorResetText(cursorUsageBar("Cursor API", usage.PlanUsage.APIPercentUsed), resetText),
	}
}

func withCursorResetText(bar ProviderUsageBar, resetText string) ProviderUsageBar {
	bar.ResetText = resetText
	return bar
}

func cursorUsageBar(label string, usedPercent float64) ProviderUsageBar {
	used := int(math.Round(clampPercent(usedPercent)))
	left := 100 - used
	if left < 0 {
		left = 0
	}
	status := "ok"
	if left <= 0 {
		status = "limit_reached"
	}
	return ProviderUsageBar{
		Label:       label,
		UsedPercent: used,
		LeftPercent: left,
		Status:      status,
	}
}

func cursorBillingCycleResetText(billingCycleEnd int64) string {
	if billingCycleEnd <= 0 {
		return ""
	}
	// Cursor returns billing cycle timestamps in milliseconds.
	resetAt := time.UnixMilli(billingCycleEnd).UTC()
	if resetAt.IsZero() {
		return ""
	}
	return "resets " + resetAt.Format(time.RFC3339)
}
