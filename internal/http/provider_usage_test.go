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

	text := formatOpenAICodexUsage(payload)
	for _, want := range []string{"Plan: plus", "Codex: allowed", "75% left", "50% left", "Credits balance: 0", "Reset credits: 2 available"} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted usage %q does not contain %q", text, want)
		}
	}
}

func TestProviderUsageStatusForAnthropicExplainsPerRunUsageOnly(t *testing.T) {
	server := &Server{config: config.DefaultConfig()}
	claudePath := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(claudePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", claudePath)
	usage := server.providerUsageStatus(context.Background(), config.ProviderAnthropic)
	if usage.Status == providerUsageStatusUnsupported {
		t.Fatalf("anthropic usage should be supported status text, got unsupported")
	}
	if !strings.Contains(usage.UsageLeftText, "per-run Claude CLI token usage") {
		t.Fatalf("anthropic usage text should mention per-run token usage, got %q", usage.UsageLeftText)
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
