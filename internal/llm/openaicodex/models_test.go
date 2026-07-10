package openaicodex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func contains(models []string, target string) bool {
	for _, m := range models {
		if m == target {
			return true
		}
	}
	return false
}

func TestNormalizeModelIDSeparatesLegacyReasoningSuffix(t *testing.T) {
	tests := map[string]string{
		"gpt-5.6-sol-medium": "gpt-5.6-sol",
		"gpt-5.6-terra-high": "gpt-5.6-terra",
		"gpt-5.6-luna-low":   "gpt-5.6-luna",
		"gpt-5.6-sol":        "gpt-5.6-sol",
		"gpt-5.5":            "gpt-5.5",
		"custom-medium":      "custom-medium",
	}
	for input, want := range tests {
		if got := NormalizeModelID(input); got != want {
			t.Errorf("NormalizeModelID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestListModelCatalogReturnsCuratedWithoutCredentials(t *testing.T) {
	models := ListModelCatalog(context.Background(), ModelCatalogOptions{})
	if len(models) != len(CuratedModels) {
		t.Fatalf("expected curated list of %d, got %d", len(CuratedModels), len(models))
	}
	for i, want := range CuratedModels {
		if models[i] != want {
			t.Fatalf("curated order changed at %d: want %q got %q", i, want, models[i])
		}
	}
	for _, want := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if !contains(models, want) {
			t.Fatalf("verified OAuth model %q missing from curated catalog: %v", want, models)
		}
	}
}

func TestListModelCatalogIgnoresOAuthOnlyCredentials(t *testing.T) {
	// Without an API key or OAuth token, discovery must not run.
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	models := ListModelCatalog(context.Background(), ModelCatalogOptions{
		BaseURL:    server.URL + "/backend-api/codex",
		HTTPClient: server.Client(),
	})
	if called {
		t.Fatalf("no discovery request should be made without credentials")
	}
	if len(models) != len(CuratedModels) {
		t.Fatalf("offline mode must return curated list only, got %v", models)
	}
	if contains(models, "gpt-5.3-codex-spark") {
		t.Fatalf("non-callable spark model must never appear, got %v", models)
	}
}

func TestListModelCatalogDoesNotDiscoverFromOAuthUsageEndpoint(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if !strings.HasSuffix(r.URL.Path, "/wham/usage") {
			t.Errorf("unexpected usage path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"additional_rate_limits": []map[string]any{
				{"limit_name": "gpt-5.6-sol-medium"},
				{"limit_name": "gpt-5.6-terra-medium"},
				{"limit_name": "gpt-5.3-codex-spark"},
				{"limit_name": "Codex"},
			},
		})
	}))
	defer server.Close()

	models := ListModelCatalog(context.Background(), ModelCatalogOptions{
		BaseURL:     server.URL + "/backend-api/codex",
		AccessToken: "oauth-token",
		HTTPClient:  server.Client(),
	})

	if called {
		t.Fatalf("OAuth usage buckets are not authoritative for callable models and should not be queried")
	}
	for _, blocked := range []string{"gpt-5.6-sol-medium", "gpt-5.6-terra-medium", "gpt-5.3-codex-spark", "Codex"} {
		if contains(models, blocked) {
			t.Fatalf("unverified OAuth usage-bucket name %q must not appear, got %v", blocked, models)
		}
	}
	for _, want := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if !contains(models, want) {
			t.Fatalf("verified OAuth model %q missing from catalog: %v", want, models)
		}
	}
	if len(models) != len(CuratedModels) {
		t.Fatalf("OAuth mode should return curated callable catalog only, got %v", models)
	}
}

func TestListModelCatalogDiscoversFromModelsEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("unexpected models path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-5.6-codex"},
				{"id": "text-embedding-3-large"}, // filtered out
			},
		})
	}))
	defer server.Close()

	models := ListModelCatalog(context.Background(), ModelCatalogOptions{
		BaseURL:    server.URL + "/v1",
		APIKey:     "sk-test",
		HTTPClient: server.Client(),
	})

	if !contains(models, "gpt-5.6-codex") {
		t.Fatalf("expected discovered api model, got %v", models)
	}
	if contains(models, "text-embedding-3-large") {
		t.Fatalf("non-codex model should be filtered out, got %v", models)
	}
}

func TestListModelCatalogIgnoresDiscoveryFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	models := ListModelCatalog(context.Background(), ModelCatalogOptions{
		BaseURL:    server.URL + "/v1",
		APIKey:     "sk-expired",
		HTTPClient: server.Client(),
	})
	if len(models) != len(CuratedModels) {
		t.Fatalf("discovery failure should fall back to curated, got %v", models)
	}
}

func TestUsageURLDerivation(t *testing.T) {
	cases := map[string]string{
		"":                                       "https://chatgpt.com/backend-api/wham/usage",
		"https://chatgpt.com/backend-api/codex":  "https://chatgpt.com/backend-api/wham/usage",
		"https://chatgpt.com/backend-api/codex/": "https://chatgpt.com/backend-api/wham/usage",
		"https://proxy.internal/backend-api/codex": "https://proxy.internal/backend-api/wham/usage",
	}
	for in, want := range cases {
		got, err := UsageURL(in)
		if err != nil {
			t.Fatalf("UsageURL(%q) error: %v", in, err)
		}
		if got != want {
			t.Fatalf("UsageURL(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := UsageURL("://bad"); err == nil {
		t.Fatalf("expected error for invalid URL")
	}
}
