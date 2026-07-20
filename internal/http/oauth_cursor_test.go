package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestProviderUsageStatusForCursorUsesParentProxyInDockerChild(t *testing.T) {
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/providers/cursor/usage" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"provider":"cursor",
			"status":"available",
			"usage_left_text":"You've used 50% of your included usage",
			"usage_bars":[{"label":"Cursor total","used_percent":50,"left_percent":50,"status":"ok"}],
			"source":"Cursor dashboard (GetCurrentPeriodUsage)",
			"refreshable":true
		}`))
	}))
	defer parent.Close()

	child := &Server{config: config.DefaultConfig()}
	t.Setenv("A2GENT_PARENT_PROXY_URL", parent.URL+"/v1")
	t.Setenv("AAGENT_CURSOR_ACCESS_TOKEN", "")
	t.Setenv("AAGENT_CURSOR_SKIP_PLATFORM_AUTH", "true")

	usage, err := child.cursorUsageStatus(context.Background())
	if err != nil {
		t.Fatalf("cursorUsageStatus returned error: %v", err)
	}
	if usage.Status != providerUsageStatusAvailable {
		t.Fatalf("status = %q, want %q: %+v", usage.Status, providerUsageStatusAvailable, usage)
	}
	if !strings.Contains(usage.UsageLeftText, "50% of your included usage") {
		t.Fatalf("unexpected usage text: %q", usage.UsageLeftText)
	}
}

func TestProviderUsageStatusForCursorUsesStoredOAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer stored-cursor-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"planUsage":{"totalPercentUsed":10,"autoPercentUsed":12,"apiPercentUsed":3},
			"displayMessage":"You've used 10% of your included usage"
		}`))
	}))
	defer server.Close()

	bruteServer := newCursorUsageTestServer(t)
	bruteServer.config.Providers[string(config.ProviderCursor)] = config.Provider{
		Name: string(config.ProviderCursor),
		OAuth: &config.OAuthConfig{
			AccessToken: "stored-cursor-token",
		},
	}
	t.Setenv("AAGENT_CURSOR_ACCESS_TOKEN", "")
	t.Setenv("AAGENT_CURSOR_SKIP_PLATFORM_AUTH", "true")
	t.Setenv("AAGENT_CURSOR_USAGE_URL", server.URL+"/aiserver.v1.DashboardService/GetCurrentPeriodUsage")

	usage := bruteServer.providerUsageStatus(context.Background(), config.ProviderCursor)
	if usage.Status != providerUsageStatusAvailable {
		t.Fatalf("status = %q, want %q: %+v", usage.Status, providerUsageStatusAvailable, usage)
	}
	if !strings.Contains(usage.UsageLeftText, "10% of your included usage") {
		t.Fatalf("unexpected usage text: %q", usage.UsageLeftText)
	}
}

func TestHandleCursorOAuthImportStoresToken(t *testing.T) {
	bruteServer := newCursorUsageTestServer(t)
	t.Setenv("AAGENT_CURSOR_ACCESS_TOKEN", "imported-cursor-token")
	t.Setenv("AAGENT_CURSOR_SKIP_PLATFORM_AUTH", "true")

	req := httptest.NewRequest(http.MethodPost, "/providers/cursor/oauth/import", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	bruteServer.handleCursorOAuthImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	provider := bruteServer.config.Providers[string(config.ProviderCursor)]
	if provider.OAuth == nil || provider.OAuth.AccessToken != "imported-cursor-token" {
		t.Fatalf("expected imported oauth token, got %+v", provider.OAuth)
	}
}
