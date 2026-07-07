package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
)

func TestOpenAICodexUsageURLUsesChatGPTWhamUsageEndpoint(t *testing.T) {
	got, err := openAICodexUsageURL("https://chatgpt.com/backend-api/codex")
	if err != nil {
		t.Fatalf("openAICodexUsageURL returned error: %v", err)
	}
	want := "https://chatgpt.com/backend-api/wham/usage"
	if got != want {
		t.Fatalf("openAICodexUsageURL = %q, want %q", got, want)
	}
}

func TestFetchOpenAICodexUsageSendsOAuthHeadersAndParsesPayload(t *testing.T) {
	accessToken := codexTestJWT(t, "acc_123")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Fatalf("path = %q, want /backend-api/wham/usage", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+accessToken {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acc_123" {
			t.Fatalf("ChatGPT-Account-Id = %q, want acc_123", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"plan_type":"plus",
			"rate_limit":{
				"allowed":true,
				"limit_reached":false,
				"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_after_seconds":3600,"reset_at":1800000000},
				"secondary_window":{"used_percent":50,"limit_window_seconds":604800,"reset_after_seconds":86400,"reset_at":1800086400}
			},
			"credits":{"has_credits":false,"unlimited":false,"balance":"0"},
			"rate_limit_reset_credits":{"available_count":2}
		}`))
	}))
	defer server.Close()

	payload, err := fetchOpenAICodexUsage(context.Background(), server.Client(), server.URL+"/backend-api/codex", accessToken)
	if err != nil {
		t.Fatalf("fetchOpenAICodexUsage returned error: %v", err)
	}
	if payload.PlanType != "plus" || payload.RateLimit == nil || payload.RateLimit.PrimaryWindow == nil {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	bars := openAICodexUsageBars(payload)
	if len(bars) != 2 {
		t.Fatalf("usage bars length = %d, want 2: %+v", len(bars), bars)
	}
	if bars[0].Label != "Codex 5h" || bars[0].UsedPercent != 25 || bars[0].LeftPercent != 75 {
		t.Fatalf("unexpected primary usage bar: %+v", bars[0])
	}
	if bars[1].Label != "Codex weekly" || bars[1].UsedPercent != 50 || bars[1].LeftPercent != 50 {
		t.Fatalf("unexpected secondary usage bar: %+v", bars[1])
	}

	text := formatOpenAICodexUsage(payload)
	for _, want := range []string{"Plan: plus", "Codex: allowed", "75% left", "50% left", "Credits balance: 0", "Reset credits: 2 available"} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted usage %q does not contain %q", text, want)
		}
	}
}

func TestOpenRouterUsageStatusReturnsCredits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/credits" {
			t.Fatalf("path = %q, want /v1/credits", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-openrouter-key" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"total_credits":10,"total_usage":2.5}}`))
	}))
	defer server.Close()

	s := &Server{config: config.DefaultConfig()}
	s.config.Providers[string(config.ProviderOpenRouter)] = config.Provider{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-openrouter-key",
	}
	usage := s.providerUsageStatus(context.Background(), config.ProviderOpenRouter)
	if usage.Status != providerUsageStatusAvailable {
		t.Fatalf("status = %q, want %q: %+v", usage.Status, providerUsageStatusAvailable, usage)
	}
	if len(usage.UsageBars) != 1 || usage.UsageBars[0].LeftPercent != 75 || usage.UsageBars[0].Status != "ok" {
		t.Fatalf("unexpected usage bars: %+v", usage.UsageBars)
	}
	if !strings.Contains(usage.UsageLeftText, "Credits remaining: 7.5") {
		t.Fatalf("unexpected usage text: %q", usage.UsageLeftText)
	}
}

func TestHealthMarksDockerSafeOpenRouterOfflineWhenCreditsExhausted(t *testing.T) {
	creditsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/credits" {
			t.Fatalf("path = %q, want /v1/credits", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"total_credits":5,"total_usage":5}}`))
	}))
	defer creditsServer.Close()

	server, cleanup := newRequestLoggingTestServer(t)
	defer cleanup()
	server.config.ActiveProvider = string(config.ProviderOpenRouter)
	server.config.Providers[string(config.ProviderOpenRouter)] = config.Provider{
		BaseURL: creditsServer.URL + "/v1",
		APIKey:  "test-openrouter-key",
	}
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://parent.example/v1")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	server.handleHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse health body: %v", err)
	}
	if body["status"] != "offline" || body["reason"] != "openrouter_usage_limit_reached" {
		t.Fatalf("unexpected health body: %+v", body)
	}
	if !strings.Contains(rec.Body.String(), "OpenRouter credits") || !strings.Contains(rec.Body.String(), "provider_usage") {
		t.Fatalf("expected OpenRouter usage details in health body: %s", rec.Body.String())
	}
}

func TestProviderUsageStatusForAnthropicReturnsCachedRateLimits(t *testing.T) {
	server := newAnthropicUsageTestServer(t)
	cachePath := filepath.Join(t.TempDir(), "claude-rate-limits.json")
	writeAnthropicRateLimitCache(t, cachePath, `{
		"rate_limits": {
			"five_hour": {"used_percentage": 37, "resets_at": 4102444800},
			"seven_day": {"used_percentage": 12, "resets_at": 4103049600}
		}
	}`, time.Now())
	t.Setenv("AAGENT_CLAUDE_RATE_LIMITS_PATH", cachePath)

	usage := server.providerUsageStatus(context.Background(), config.ProviderAnthropic)
	if usage.Status != providerUsageStatusAvailable {
		t.Fatalf("status = %q, want %q: %+v", usage.Status, providerUsageStatusAvailable, usage)
	}
	if usage.Source != anthropicUsageSource {
		t.Fatalf("source = %q, want %q", usage.Source, anthropicUsageSource)
	}
	if len(usage.UsageBars) != 2 {
		t.Fatalf("usage bars length = %d, want 2: %+v", len(usage.UsageBars), usage.UsageBars)
	}
	if usage.UsageBars[0].Label != "Claude 5h" || usage.UsageBars[0].UsedPercent != 37 || usage.UsageBars[0].LeftPercent != 63 {
		t.Fatalf("unexpected 5h usage bar: %+v", usage.UsageBars[0])
	}
	if usage.UsageBars[1].Label != "Claude weekly" || usage.UsageBars[1].UsedPercent != 12 || usage.UsageBars[1].LeftPercent != 88 {
		t.Fatalf("unexpected weekly usage bar: %+v", usage.UsageBars[1])
	}
	if usage.UsageBars[0].ResetText == "" || usage.UsageBars[1].ResetText == "" {
		t.Fatalf("expected reset text on all bars: %+v", usage.UsageBars)
	}
	if !strings.Contains(usage.UsageLeftText, "5h 63% left") || !strings.Contains(usage.UsageLeftText, "weekly 88% left") {
		t.Fatalf("expected usage summary in %q", usage.UsageLeftText)
	}
	if usage.CheckedAt == "" {
		t.Fatal("expected checked_at to be set from cache snapshot")
	}
}

func TestProviderUsageStatusForAnthropicReturnsUnavailableWhenCacheMissing(t *testing.T) {
	server := newAnthropicUsageTestServer(t)
	cachePath := filepath.Join(t.TempDir(), "missing-rate-limits.json")
	t.Setenv("AAGENT_CLAUDE_RATE_LIMITS_PATH", cachePath)

	usage := server.providerUsageStatus(context.Background(), config.ProviderAnthropic)
	if usage.Status != providerUsageStatusUnavailable {
		t.Fatalf("status = %q, want %q", usage.Status, providerUsageStatusUnavailable)
	}
	if len(usage.UsageBars) != 0 {
		t.Fatalf("expected no usage bars for missing cache, got %+v", usage.UsageBars)
	}
	if !strings.Contains(usage.UsageLeftText, "not found") {
		t.Fatalf("expected missing-cache explanation, got %q", usage.UsageLeftText)
	}
}

func TestProviderUsageStatusForAnthropicReturnsUnavailableWhenCacheIsStale(t *testing.T) {
	server := newAnthropicUsageTestServer(t)
	cachePath := filepath.Join(t.TempDir(), "stale-rate-limits.json")
	writeAnthropicRateLimitCache(t, cachePath, `{
		"rate_limits": {
			"five_hour": {"used_percentage": 37, "resets_at": 4102444800},
			"seven_day": {"used_percentage": 12, "resets_at": 4103049600}
		}
	}`, time.Now().Add(-48*time.Hour))
	t.Setenv("AAGENT_CLAUDE_RATE_LIMITS_PATH", cachePath)

	usage := server.providerUsageStatus(context.Background(), config.ProviderAnthropic)
	if usage.Status != providerUsageStatusUnavailable {
		t.Fatalf("status = %q, want %q", usage.Status, providerUsageStatusUnavailable)
	}
	if len(usage.UsageBars) != 0 {
		t.Fatalf("expected no usage bars for stale cache, got %+v", usage.UsageBars)
	}
	if !strings.Contains(usage.UsageLeftText, "stale") {
		t.Fatalf("expected stale-cache explanation, got %q", usage.UsageLeftText)
	}
}

func TestProviderUsageLimitReachedIgnoresElapsedReset(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	usage := ProviderUsageResponse{
		Status: providerUsageStatusAvailable,
		UsageBars: []ProviderUsageBar{{
			Label:       "Claude 5h",
			LeftPercent: 0,
			Status:      "limit_reached",
			ResetText:   "resets " + now.Add(-time.Minute).Format(time.RFC3339),
		}},
	}
	if reached, detail := providerUsageLimitReached(usage, now); reached {
		t.Fatalf("expected elapsed reset to be non-fatal, got reached with detail %q", detail)
	}

	usage.UsageBars[0].ResetText = "resets " + now.Add(time.Minute).Format(time.RFC3339)
	if reached, detail := providerUsageLimitReached(usage, now); !reached || !strings.Contains(detail, "Claude 5h") {
		t.Fatalf("expected future reset to be fatal, got reached=%v detail=%q", reached, detail)
	}
}

func TestHealthReturnsOfflineForDockerSafeAnthropicLimitReached(t *testing.T) {
	server, cleanup := newRequestLoggingTestServer(t)
	defer cleanup()
	server.config.ActiveProvider = string(config.ProviderAnthropic)
	cachePath := filepath.Join(t.TempDir(), "claude-rate-limits.json")
	writeAnthropicRateLimitCache(t, cachePath, `{
		"rate_limits": {
			"five_hour": {"used_percentage": 100, "resets_at": 4102444800},
			"seven_day": {"used_percentage": 25, "resets_at": 4103049600}
		}
	}`, time.Now())
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://parent.example/v1")
	t.Setenv(claudeRateLimitsCachePathEnv, cachePath)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	server.handleHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse health body: %v", err)
	}
	if body["status"] != "offline" || body["reason"] != "anthropic_usage_limit_reached" {
		t.Fatalf("unexpected health body: %+v", body)
	}
	if !strings.Contains(rec.Body.String(), "anthropic usage limit reached") || !strings.Contains(rec.Body.String(), "provider_usage") {
		t.Fatalf("expected usage details in health body: %s", rec.Body.String())
	}
}

func TestHealthMarksDockerSafeCodexOfflineWhenUsageLimitReached(t *testing.T) {
	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wham/usage" {
			t.Fatalf("unexpected usage path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"plan_type":"plus",
			"rate_limit":{
				"allowed":false,
				"limit_reached":true,
				"primary_window":{"used_percent":100,"reset_at":4102444800}
			}
		}`))
	}))
	defer usageServer.Close()

	server, cleanup := newRequestLoggingTestServer(t)
	defer cleanup()
	server.config.ActiveProvider = string(config.ProviderOpenAICodex)
	server.config.Providers[string(config.ProviderOpenAICodex)] = config.Provider{
		BaseURL: usageServer.URL,
		OAuth:   &config.OAuthConfig{AccessToken: "header.payload.signature"},
	}
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://parent.example/v1")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	server.handleHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse health body: %v", err)
	}
	if body["status"] != "offline" || body["reason"] != "openai_codex_usage_limit_reached" {
		t.Fatalf("unexpected health body: %+v", body)
	}
	if !strings.Contains(rec.Body.String(), "openai_codex usage limit reached") || !strings.Contains(rec.Body.String(), "provider_usage") {
		t.Fatalf("expected usage details in health body: %s", rec.Body.String())
	}
}

func TestHealthStaysOKForDockerSafeAnthropicWhenUsageUnavailable(t *testing.T) {
	server, cleanup := newRequestLoggingTestServer(t)
	defer cleanup()
	server.config.ActiveProvider = string(config.ProviderAnthropic)
	t.Setenv("A2GENT_PARENT_PROXY_URL", "http://parent.example/v1")
	t.Setenv(claudeRateLimitsCachePathEnv, filepath.Join(t.TempDir(), "missing-rate-limits.json"))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	server.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) || !strings.Contains(rec.Body.String(), `"provider_usage"`) {
		t.Fatalf("expected ok health body with usage diagnostics: %s", rec.Body.String())
	}
}

func TestAppendClaudeRateLimitCacheDockerArgsMountsExistingCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "claude-rate-limits.json")
	writeAnthropicRateLimitCache(t, cachePath, `{
		"rate_limits": {
			"five_hour": {"used_percentage": 42, "resets_at": 4102444800}
		}
	}`, time.Now())
	t.Setenv(claudeRateLimitsCachePathEnv, cachePath)
	t.Setenv(claudeRateLimitsCacheMaxAgeEnv, "30m")

	got := appendClaudeRateLimitCacheDockerArgs([]string{"run"})
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, cachePath+":"+containerClaudeRateLimitsCachePath+":ro") {
		t.Fatalf("expected cache volume mount in args: %#v", got)
	}
	if !strings.Contains(joined, claudeRateLimitsCachePathEnv+"="+containerClaudeRateLimitsCachePath) {
		t.Fatalf("expected container cache env in args: %#v", got)
	}
	if !strings.Contains(joined, claudeRateLimitsCacheMaxAgeEnv+"=30m") {
		t.Fatalf("expected cache max-age env in args: %#v", got)
	}
}

func newAnthropicUsageTestServer(t *testing.T) *Server {
	t.Helper()
	claudePath := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", claudePath)
	return &Server{config: config.DefaultConfig()}
}

func writeAnthropicRateLimitCache(t *testing.T, path, body string, modTime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func codexTestJWT(t *testing.T, accountID string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims, err := json.Marshal(map[string]interface{}{
		"https://api.openai.com/auth": map[string]interface{}{
			"chatgpt_account_id": accountID,
		},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(claims)
	return header + "." + payload + ".signature"
}
