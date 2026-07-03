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
}

func TestListModelCatalogIgnoresOAuthOnlyCredentials(t *testing.T) {
	// A ChatGPT-account (OAuth) session provides no API key. Discovery must not
	// run, and the catalog must be exactly the curated list — the usage endpoint
	// surfaces quota buckets (e.g. gpt-5.3-codex-spark) that are NOT callable.
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
		t.Fatalf("no discovery request should be made without an API key")
	}
	if len(models) != len(CuratedModels) {
		t.Fatalf("OAuth mode must return curated list only, got %v", models)
	}
	if contains(models, "gpt-5.3-codex-spark") {
		t.Fatalf("non-callable spark model must never appear, got %v", models)
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
