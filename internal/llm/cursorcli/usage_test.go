package cursorcli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchPeriodUsageParsesPlanUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/aiserver.v1.DashboardService/GetCurrentPeriodUsage" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Connect-Protocol-Version"); got != "1" {
			t.Fatalf("Connect-Protocol-Version = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"billingCycleStart": 1783419501000,
			"billingCycleEnd": 1786097901000,
			"planUsage": {
				"autoPercentUsed": 26.32,
				"apiPercentUsed": 13.5,
				"totalPercentUsed": 20.25,
				"limit": 2000,
				"totalSpend": 404,
				"includedSpend": 200
			},
			"displayMessage": "You've used 20% of your included usage"
		}`))
	}))
	defer server.Close()

	usage, err := FetchPeriodUsage(context.Background(), server.Client(), server.URL+"/aiserver.v1.DashboardService/GetCurrentPeriodUsage", "test-token")
	if err != nil {
		t.Fatalf("FetchPeriodUsage returned error: %v", err)
	}
	if usage.PlanUsage.AutoPercentUsed != 26.32 || usage.PlanUsage.APIPercentUsed != 13.5 || usage.PlanUsage.TotalPercentUsed != 20.25 {
		t.Fatalf("unexpected plan usage: %+v", usage.PlanUsage)
	}
	if usage.BillingCycleEnd != 1786097901000 {
		t.Fatalf("billingCycleEnd = %d", usage.BillingCycleEnd)
	}
	if usage.DisplayMessage != "You've used 20% of your included usage" {
		t.Fatalf("displayMessage = %q", usage.DisplayMessage)
	}
}

// Cursor's Connect RPC now returns billing cycle timestamps as JSON strings
// (e.g. "1783419501000") while older responses used numeric millis.
func TestFetchPeriodUsageAcceptsStringBillingCycleTimestamps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"billingCycleStart": "1783419501000",
			"billingCycleEnd": "1786097901000",
			"planUsage": {
				"autoPercentUsed": 66.61333333333333,
				"apiPercentUsed": 1.7777777777777777,
				"totalPercentUsed": 58.15652173913044
			},
			"displayMessage": "You've used 50% of your included usage"
		}`))
	}))
	defer server.Close()

	usage, err := FetchPeriodUsage(context.Background(), server.Client(), server.URL+"/aiserver.v1.DashboardService/GetCurrentPeriodUsage", "test-token")
	if err != nil {
		t.Fatalf("FetchPeriodUsage returned error: %v", err)
	}
	if usage.BillingCycleStart != 1783419501000 {
		t.Fatalf("billingCycleStart = %d, want 1783419501000", usage.BillingCycleStart)
	}
	if usage.BillingCycleEnd != 1786097901000 {
		t.Fatalf("billingCycleEnd = %d, want 1786097901000", usage.BillingCycleEnd)
	}
	if usage.PlanUsage.TotalPercentUsed != 58.15652173913044 {
		t.Fatalf("unexpected plan usage: %+v", usage.PlanUsage)
	}
	if usage.DisplayMessage != "You've used 50% of your included usage" {
		t.Fatalf("displayMessage = %q", usage.DisplayMessage)
	}
}

func TestResolveAccessTokenFromAuthFile(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	body, err := json.Marshal(map[string]string{
		"accessToken": "file-access-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	token, err := resolveAccessTokenFromAuthFile(authPath)
	if err != nil {
		t.Fatalf("resolveAccessTokenFromAuthFile returned error: %v", err)
	}
	if token != "file-access-token" {
		t.Fatalf("token = %q, want file-access-token", token)
	}
}

func TestResolveAccessTokenPrefersEnvOverride(t *testing.T) {
	t.Setenv(cursorAccessTokenEnv, "env-access-token")
	token, err := ResolveAccessToken()
	if err != nil {
		t.Fatalf("ResolveAccessToken returned error: %v", err)
	}
	if token != "env-access-token" {
		t.Fatalf("token = %q, want env-access-token", token)
	}
}

func TestFormatPeriodUsageSummary(t *testing.T) {
	text := FormatPeriodUsageSummary(PeriodUsage{
		DisplayMessage: "You've used 20% of your included usage",
		PlanUsage: PlanUsage{
			AutoPercentUsed:  26.32,
			APIPercentUsed:   13.5,
			TotalPercentUsed: 20.25,
		},
	})
	for _, want := range []string{
		"You've used 20% of your included usage",
		"total 80% left",
		"auto 74% left",
		"api 87% left",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary %q does not contain %q", text, want)
		}
	}
}
