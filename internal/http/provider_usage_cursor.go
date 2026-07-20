package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
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

	// WHY: Docker sub-agents cannot read the host macOS keychain. Delegate usage to
	// the parent Brute server that owns the Cursor login session.
	if s.parentProxyAvailable() {
		return s.fetchParentProviderUsage(ctx, config.ProviderCursor)
	}

	if !s.providerConfiguredForUse(config.ProviderCursor) {
		response.Status = providerUsageStatusNotConfigured
		response.UsageLeftText = "Usage left unavailable - Cursor Agent CLI is not available. Install Cursor CLI with `curl https://cursor.com/install -fsS | bash` or set AAGENT_CURSOR_CLI_PATH."
		return response, nil
	}

	accessToken, err := s.cursorUsageAccessToken()
	if err != nil {
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable - " + err.Error()
		return response, nil
	}

	usage, err := cursorcli.FetchPeriodUsage(ctx, nil, "", accessToken)
	if err != nil {
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable - failed to fetch Cursor plan usage from GetCurrentPeriodUsage. Brute will still surface quota or billing errors when requests fail."
		response.Error = err.Error()
		return response, err
	}

	response.Status = providerUsageStatusAvailable
	response.UsageLeftText = cursorcli.FormatPeriodUsageSummary(usage)
	response.UsageBars = cursorUsageBars(usage)
	return response, nil
}

func (s *Server) cursorUsageAccessToken() (string, error) {
	s.syncCursorOAuthFromPlatform()

	provider := s.config.Providers[string(config.ProviderCursor)]
	if provider.OAuth != nil {
		if token := strings.TrimSpace(provider.OAuth.AccessToken); token != "" {
			return token, nil
		}
	}

	token, err := cursorcli.ResolveAccessToken()
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Server) fetchParentProviderUsage(ctx context.Context, providerType config.ProviderType) (ProviderUsageResponse, error) {
	parentProxyURL := strings.TrimRight(strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")), "/")
	parentBase := strings.TrimSuffix(parentProxyURL, "/v1")
	if strings.TrimSpace(parentBase) == "" {
		return ProviderUsageResponse{}, fmt.Errorf("parent proxy URL is not configured")
	}

	usageURL := parentBase + "/providers/" + string(providerType) + "/usage"
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, usageURL, nil)
	if err != nil {
		return ProviderUsageResponse{}, err
	}
	req.Header.Set("Accept", "application/json")

	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return ProviderUsageResponse{}, fmt.Errorf("failed to fetch parent provider usage: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return ProviderUsageResponse{}, fmt.Errorf("failed to read parent provider usage response: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ProviderUsageResponse{}, fmt.Errorf("parent provider usage unavailable (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var usage ProviderUsageResponse
	if err := json.Unmarshal(body, &usage); err != nil {
		return ProviderUsageResponse{}, fmt.Errorf("failed to parse parent provider usage response: %w", err)
	}
	if usage.Provider == "" {
		usage.Provider = string(providerType)
	}
	if usage.CheckedAt == "" {
		usage.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return usage, nil
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
