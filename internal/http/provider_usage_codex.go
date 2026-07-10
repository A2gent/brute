package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/openaicodex"
)

const codexUsageRequestTimeout = 15 * time.Second

type codexUsageHTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type codexUsageRateLimitResponse struct {
	PlanType              string                        `json:"plan_type"`
	RateLimit             *codexUsageRateLimitDetails   `json:"rate_limit"`
	Credits               *codexUsageCredits            `json:"credits"`
	AdditionalRateLimits  []codexUsageAdditionalLimit   `json:"additional_rate_limits"`
	RateLimitResetCredits *codexUsageResetCreditSummary `json:"rate_limit_reset_credits"`
	RateLimitReachedType  *codexUsageRateLimitReached   `json:"rate_limit_reached_type"`
}

type codexUsageRateLimitDetails struct {
	Allowed         bool              `json:"allowed"`
	LimitReached    bool              `json:"limit_reached"`
	PrimaryWindow   *codexUsageWindow `json:"primary_window"`
	SecondaryWindow *codexUsageWindow `json:"secondary_window"`
}

type codexUsageWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type codexUsageCredits struct {
	HasCredits bool    `json:"has_credits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance"`
}

type codexUsageAdditionalLimit struct {
	LimitName      string                      `json:"limit_name"`
	MeteredFeature string                      `json:"metered_feature"`
	RateLimit      *codexUsageRateLimitDetails `json:"rate_limit"`
}

type codexUsageResetCreditSummary struct {
	AvailableCount int64 `json:"available_count"`
}

type codexUsageRateLimitReached struct {
	Kind string `json:"type"`
}

func (s *Server) openAICodexUsageStatus(ctx context.Context) (ProviderUsageResponse, error) {
	response := ProviderUsageResponse{
		Provider:    string(config.ProviderOpenAICodex),
		Source:      "OpenAI Codex OAuth (ChatGPT usage)",
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
		Refreshable: true,
	}

	if !s.providerConfiguredForUse(config.ProviderOpenAICodex) {
		response.Status = providerUsageStatusNotConfigured
		response.UsageLeftText = "Usage left unavailable — connect OpenAI Codex OAuth or add an API key first."
		return response, nil
	}

	oauth, baseURL, err := s.openAICodexUsageAuth()
	if err != nil {
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = err.Error()
		return response, nil
	}

	payload, err := fetchOpenAICodexUsage(ctx, http.DefaultClient, baseURL, oauth.AccessToken)
	if err != nil {
		return response, err
	}
	response.Status = providerUsageStatusAvailable
	response.UsageLeftText = formatOpenAICodexUsage(payload)
	response.UsageBars = openAICodexUsageBars(payload)
	return response, nil
}

func (s *Server) openAICodexUsageAuth() (*config.OAuthConfig, string, error) {
	provider := s.config.Providers[string(config.ProviderOpenAICodex)]
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		if def := config.GetProviderDefinition(config.ProviderOpenAICodex); def != nil {
			baseURL = def.DefaultURL
		}
	}

	if provider.OAuth != nil && strings.TrimSpace(provider.OAuth.AccessToken) != "" {
		return provider.OAuth, baseURL, nil
	}
	return nil, baseURL, fmt.Errorf("Usage left unavailable — connect OpenAI Codex OAuth first. API-key Codex mode can run requests, but ChatGPT plan usage is only available through Codex OAuth.")
}

func fetchOpenAICodexUsage(ctx context.Context, client codexUsageHTTPClient, codexBaseURL, accessToken string) (codexUsageRateLimitResponse, error) {
	if client == nil {
		client = http.DefaultClient
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return codexUsageRateLimitResponse{}, fmt.Errorf("missing Codex OAuth access token")
	}

	usageURL, err := openAICodexUsageURL(codexBaseURL)
	if err != nil {
		return codexUsageRateLimitResponse{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, codexUsageRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, usageURL, nil)
	if err != nil {
		return codexUsageRateLimitResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("User-agent", "codex_cli_rs/0.143.0")
	if accountID := extractCodexAccountID(accessToken); accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return codexUsageRateLimitResponse{}, fmt.Errorf("failed to fetch Codex usage: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return codexUsageRateLimitResponse{}, fmt.Errorf("failed to read Codex usage response: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return codexUsageRateLimitResponse{}, fmt.Errorf("Codex usage unavailable (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload codexUsageRateLimitResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return codexUsageRateLimitResponse{}, fmt.Errorf("failed to parse Codex usage response: %w", err)
	}
	return payload, nil
}

func openAICodexUsageURL(codexBaseURL string) (string, error) {
	// Shared with model discovery so both derive the usage endpoint identically.
	return openaicodex.UsageURL(codexBaseURL)
}

func openAICodexUsageBars(payload codexUsageRateLimitResponse) []ProviderUsageBar {
	bars := make([]ProviderUsageBar, 0, 4)
	appendLimitBars := func(label string, details *codexUsageRateLimitDetails) {
		if details == nil {
			return
		}
		status := "ok"
		if details.LimitReached || !details.Allowed {
			status = "limit_reached"
		}
		if details.PrimaryWindow != nil {
			bars = append(bars, codexWindowUsageBar(label+" 5h", details.PrimaryWindow, status))
		}
		if details.SecondaryWindow != nil {
			bars = append(bars, codexWindowUsageBar(label+" weekly", details.SecondaryWindow, status))
		}
	}

	appendLimitBars("Codex", payload.RateLimit)
	for _, item := range codexCallableAdditionalLimits(payload.AdditionalRateLimits) {
		appendLimitBars(codexAdditionalLimitLabel(item), item.RateLimit)
	}
	return bars
}

func codexCallableAdditionalLimits(items []codexUsageAdditionalLimit) []codexUsageAdditionalLimit {
	additional := make([]codexUsageAdditionalLimit, 0, len(items))
	for _, item := range items {
		if item.RateLimit == nil {
			continue
		}
		if openaicodex.IsNonCallableOAuthUsageLimit(item.LimitName, item.MeteredFeature) {
			continue
		}
		additional = append(additional, item)
	}
	sort.SliceStable(additional, func(i, j int) bool {
		return codexAdditionalLimitLabel(additional[i]) < codexAdditionalLimitLabel(additional[j])
	})
	return additional
}

func codexWindowUsageBar(label string, window *codexUsageWindow, status string) ProviderUsageBar {
	used := int(math.Round(clampPercent(window.UsedPercent)))
	left := 100 - used
	if left < 0 {
		left = 0
	}
	bar := ProviderUsageBar{
		Label:       label,
		UsedPercent: used,
		LeftPercent: left,
		ResetText:   codexUsageWindowResetText(window),
		Status:      status,
	}
	return bar
}

func codexUsageWindowResetText(window *codexUsageWindow) string {
	if window == nil {
		return ""
	}
	if window.ResetAt > 0 {
		return "resets " + time.Unix(window.ResetAt, 0).UTC().Format(time.RFC3339)
	}
	if window.ResetAfterSeconds > 0 {
		return fmt.Sprintf("resets in %s", formatDurationApprox(time.Duration(window.ResetAfterSeconds)*time.Second))
	}
	return ""
}

func formatOpenAICodexUsage(payload codexUsageRateLimitResponse) string {
	parts := make([]string, 0, 8)
	if plan := strings.TrimSpace(payload.PlanType); plan != "" {
		parts = append(parts, "Plan: "+plan)
	}
	if payload.RateLimit != nil {
		parts = append(parts, formatCodexUsageLimit("Codex", payload.RateLimit))
	}

	for _, item := range codexCallableAdditionalLimits(payload.AdditionalRateLimits) {
		parts = append(parts, formatCodexUsageLimit(codexAdditionalLimitLabel(item), item.RateLimit))
	}

	if payload.Credits != nil {
		parts = append(parts, formatCodexCredits(payload.Credits))
	}
	if payload.RateLimitResetCredits != nil {
		parts = append(parts, fmt.Sprintf("Reset credits: %d available", payload.RateLimitResetCredits.AvailableCount))
	}
	if payload.RateLimitReachedType != nil && strings.TrimSpace(payload.RateLimitReachedType.Kind) != "" {
		parts = append(parts, "Limit state: "+strings.TrimSpace(payload.RateLimitReachedType.Kind))
	}
	if len(parts) == 0 {
		return "Codex usage endpoint responded but did not include rate-limit details."
	}
	return strings.Join(parts, " · ")
}

func formatCodexUsageLimit(label string, details *codexUsageRateLimitDetails) string {
	if details == nil {
		return label + ": no rate-limit data"
	}
	windows := make([]string, 0, 2)
	if details.PrimaryWindow != nil {
		windows = append(windows, "5h "+formatCodexUsageWindow(details.PrimaryWindow))
	}
	if details.SecondaryWindow != nil {
		windows = append(windows, "weekly "+formatCodexUsageWindow(details.SecondaryWindow))
	}
	state := "allowed"
	if details.LimitReached || !details.Allowed {
		state = "limit reached"
	}
	if len(windows) == 0 {
		return fmt.Sprintf("%s: %s", label, state)
	}
	return fmt.Sprintf("%s: %s (%s)", label, state, strings.Join(windows, ", "))
}

func formatCodexUsageWindow(window *codexUsageWindow) string {
	used := clampPercent(window.UsedPercent)
	left := int(math.Max(0, math.Round(100-used)))
	parts := []string{fmt.Sprintf("%d%% left", left)}
	if window.ResetAt > 0 {
		parts = append(parts, "resets "+time.Unix(window.ResetAt, 0).UTC().Format(time.RFC3339))
	} else if window.ResetAfterSeconds > 0 {
		parts = append(parts, fmt.Sprintf("resets in %s", formatDurationApprox(time.Duration(window.ResetAfterSeconds)*time.Second)))
	}
	return strings.Join(parts, ", ")
}

func clampPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func formatDurationApprox(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	if d < time.Hour {
		return d.Round(time.Minute).String()
	}
	return d.Round(time.Hour).String()
}

func codexAdditionalLimitLabel(item codexUsageAdditionalLimit) string {
	if label := strings.TrimSpace(item.LimitName); label != "" {
		return label
	}
	if label := strings.TrimSpace(item.MeteredFeature); label != "" {
		return label
	}
	return "Additional limit"
}

func formatCodexCredits(credits *codexUsageCredits) string {
	if credits.Unlimited {
		return "Credits: unlimited"
	}
	if credits.Balance != nil && strings.TrimSpace(*credits.Balance) != "" {
		return "Credits balance: " + strings.TrimSpace(*credits.Balance)
	}
	if credits.HasCredits {
		return "Credits: available"
	}
	return "Credits: none"
}

func extractCodexAccountID(accessToken string) string {
	parts := strings.Split(strings.TrimSpace(accessToken), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64RawURLDecode(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	if id := stringClaim(claims["chatgpt_account_id"]); id != "" {
		return id
	}
	if raw, ok := claims["https://api.openai.com/auth"].(map[string]interface{}); ok {
		return stringClaim(raw["chatgpt_account_id"])
	}
	return ""
}

func base64RawURLDecode(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(value)
}

func stringClaim(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}
