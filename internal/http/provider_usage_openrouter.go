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
)

const (
	openRouterCreditsSource         = "OpenRouter credits API"
	openRouterCreditsRequestTimeout = 15 * time.Second
)

type openRouterCreditsResponse struct {
	Data openRouterCreditsData `json:"data"`
}

type openRouterCreditsData struct {
	TotalCredits float64 `json:"total_credits"`
	TotalUsage   float64 `json:"total_usage"`
}

type openRouterCreditsHTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func (s *Server) openRouterUsageStatus(ctx context.Context) ProviderUsageResponse {
	response := ProviderUsageResponse{
		Provider:    string(config.ProviderOpenRouter),
		Source:      openRouterCreditsSource,
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
		Refreshable: true,
	}

	if !s.providerConfiguredForUse(config.ProviderOpenRouter) {
		response.Status = providerUsageStatusNotConfigured
		response.UsageLeftText = "Usage left unavailable — configure an OpenRouter API key first."
		return response
	}

	baseURL, apiKey, err := s.openRouterCreditsAuth()
	if err != nil {
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = err.Error()
		return response
	}

	payload, err := fetchOpenRouterCredits(ctx, http.DefaultClient, baseURL, apiKey)
	if err != nil {
		response.Status = providerUsageStatusUnavailable
		response.UsageLeftText = "Usage left unavailable — failed to fetch OpenRouter credits. Brute will still surface credit or billing errors when requests fail."
		response.Error = err.Error()
		return response
	}

	bar := openRouterCreditsUsageBar(payload)
	response.Status = providerUsageStatusAvailable
	response.UsageLeftText = formatOpenRouterCreditsUsage(payload)
	response.UsageBars = []ProviderUsageBar{bar}
	return response
}

func (s *Server) openRouterCreditsAuth() (string, string, error) {
	provider := s.config.Providers[string(config.ProviderOpenRouter)]
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		if def := config.GetProviderDefinition(config.ProviderOpenRouter); def != nil {
			baseURL = strings.TrimSpace(def.DefaultURL)
		}
	}
	if envURL := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")); envURL != "" {
		baseURL = envURL
	}
	baseURL = normalizeOpenAIBaseURL(baseURL)
	if baseURL == "" {
		return "", "", fmt.Errorf("Usage left unavailable — OpenRouter base URL is empty")
	}

	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(config.ProviderOpenRouter)
	}
	if apiKey == "" {
		return "", "", fmt.Errorf("Usage left unavailable — configure an OpenRouter API key first")
	}
	return baseURL, apiKey, nil
}

func fetchOpenRouterCredits(ctx context.Context, client openRouterCreditsHTTPClient, baseURL, apiKey string) (openRouterCreditsResponse, error) {
	if client == nil {
		client = http.DefaultClient
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return openRouterCreditsResponse{}, fmt.Errorf("missing OpenRouter API key")
	}
	creditsURL := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/credits"
	requestCtx, cancel := context.WithTimeout(ctx, openRouterCreditsRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, creditsURL, nil)
	if err != nil {
		return openRouterCreditsResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return openRouterCreditsResponse{}, fmt.Errorf("failed to fetch OpenRouter credits: %w", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return openRouterCreditsResponse{}, fmt.Errorf("failed to read OpenRouter credits response: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return openRouterCreditsResponse{}, fmt.Errorf("OpenRouter credits unavailable (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload openRouterCreditsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return openRouterCreditsResponse{}, fmt.Errorf("failed to parse OpenRouter credits response: %w", err)
	}
	return payload, nil
}

func openRouterCreditsUsageBar(payload openRouterCreditsResponse) ProviderUsageBar {
	total := payload.Data.TotalCredits
	used := payload.Data.TotalUsage
	remaining := total - used
	usedPercent := 100
	leftPercent := 0
	if total > 0 {
		usedPercent = int(math.Round(clampPercent((used / total) * 100)))
		leftPercent = 100 - usedPercent
		if leftPercent < 0 {
			leftPercent = 0
		}
	}
	status := "ok"
	if remaining <= 0 {
		status = "limit_reached"
		leftPercent = 0
		if usedPercent < 100 {
			usedPercent = 100
		}
	}
	return ProviderUsageBar{
		Label:       "OpenRouter credits",
		UsedPercent: usedPercent,
		LeftPercent: leftPercent,
		Status:      status,
	}
}

func formatOpenRouterCreditsUsage(payload openRouterCreditsResponse) string {
	total := payload.Data.TotalCredits
	used := payload.Data.TotalUsage
	remaining := total - used
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Sprintf("Credits remaining: %s (total %s, used %s)", formatOpenRouterCreditAmount(remaining), formatOpenRouterCreditAmount(total), formatOpenRouterCreditAmount(used))
}

func formatOpenRouterCreditAmount(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", value), "0"), ".")
}
