package antigravitycli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchQuotaSummaryParsesBuckets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != quotaSummaryUserAgent {
			t.Errorf("User-Agent = %q, want %q", r.Header.Get("User-Agent"), quotaSummaryUserAgent)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"groups":[{"displayName":"Claude and GPT models","buckets":[
			{"bucketId":"3p-weekly","window":"weekly","resetTime":"2026-11-27T20:41:04Z","remainingFraction":0.25},
			{"bucketId":"3p-5h","window":"5h","resetTime":"2026-11-21T01:41:04Z","remainingFraction":1}]}]}`))
	}))
	defer server.Close()

	summary, err := FetchQuotaSummary(context.Background(), nil, server.URL, "test-token")
	if err != nil {
		t.Fatalf("FetchQuotaSummary: %v", err)
	}
	if len(summary.Groups) != 1 || len(summary.Groups[0].Buckets) != 2 {
		t.Fatalf("unexpected groups: %+v", summary.Groups)
	}
	weekly := summary.Groups[0].Buckets[0]
	if weekly.Window != "weekly" || weekly.RemainingFraction != 0.25 || weekly.ResetTime != "2026-11-27T20:41:04Z" {
		t.Fatalf("weekly bucket = %+v", weekly)
	}
}

func TestFetchQuotaSummaryReportsHTTPErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	_, err := FetchQuotaSummary(context.Background(), nil, server.URL, "test-token")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want 403 error", err)
	}
}

func TestResolveAccessTokenPrefersEnvOverride(t *testing.T) {
	t.Setenv(accessTokenEnv, " env-token ")
	token, err := ResolveAccessToken()
	if err != nil || token != "env-token" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
}
