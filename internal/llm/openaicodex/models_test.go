package openaicodex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testOAuthToken(accountID string) string {
	payload, _ := json.Marshal(map[string]string{"chatgpt_account_id": accountID})
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

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
	if models[0] != "gpt-6-astra" {
		t.Fatalf("newest Codex/OpenAI flagship should lead the catalog, got %q", models[0])
	}
	for i, want := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		if models[i] != want {
			t.Fatalf("official flagship order at %d: want %q got %q", i, want, models[i])
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

func TestListModelCatalogOAuthNormalizesResponsesBaseURL(t *testing.T) {
	const accountID = "acct-test-123"
	token := testOAuthToken(accountID)
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{"slug": "gpt-6-terra"}},
		})
	}))
	defer server.Close()

	models := ListModelCatalog(context.Background(), ModelCatalogOptions{
		BaseURL:     server.URL + "/backend-api/codex/responses",
		AccessToken: token,
		HTTPClient:  server.Client(),
	})
	if requestedPath != "/backend-api/codex/models" {
		t.Fatalf("OAuth discovery path = %q, want /backend-api/codex/models", requestedPath)
	}
	if !contains(models, "gpt-6-terra") {
		t.Fatalf("OAuth-discovered model gpt-6-terra missing from catalog: %v", models)
	}
}

func TestListModelCatalogDiscoversFromOAuthModelsEndpoint(t *testing.T) {
	const accountID = "acct-test-123"
	token := testOAuthToken(accountID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("client_version") != ClientVersion {
			t.Fatalf("unexpected client_version: %q", r.URL.Query().Get("client_version"))
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+token {
			t.Fatalf("unexpected Authorization: %q", auth)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Fatalf("unexpected Accept: %q", r.Header.Get("Accept"))
		}
		if r.Header.Get("Originator") != "codex_cli_rs" {
			t.Fatalf("unexpected Originator: %q", r.Header.Get("Originator"))
		}
		if r.Header.Get("User-Agent") != UserAgent() {
			t.Fatalf("unexpected User-Agent: %q", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("ChatGPT-Account-Id") != accountID {
			t.Fatalf("unexpected ChatGPT-Account-Id: %q", r.Header.Get("ChatGPT-Account-Id"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"slug": "gpt-6-terra"},
				{"slug": "gpt-5.6-codex"},
				{"slug": "text-embedding-3-large"},
				{"slug": "Codex"},
			},
		})
	}))
	defer server.Close()

	models := ListModelCatalog(context.Background(), ModelCatalogOptions{
		BaseURL:     server.URL + "/backend-api/codex",
		AccessToken: token,
		HTTPClient:  server.Client(),
	})

	if !contains(models, "gpt-6-terra") {
		t.Fatalf("OAuth-discovered model gpt-6-terra missing from catalog: %v", models)
	}
	if !contains(models, "gpt-5.6-codex") {
		t.Fatalf("OAuth-discovered model gpt-5.6-codex missing from catalog: %v", models)
	}
	for _, blocked := range []string{"text-embedding-3-large", "Codex"} {
		if contains(models, blocked) {
			t.Fatalf("filtered slug %q must not appear, got %v", blocked, models)
		}
	}
}

func TestListModelCatalogOAuthFiltersHiddenModels(t *testing.T) {
	token := testOAuthToken("acct-test-123")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"slug": "gpt-6-terra", "visibility": "list"},
				{"slug": "gpt-6-shadow", "visibility": "hide"},
				{"slug": "gpt-6-ghost", "visibility": "none"},
				{"slug": "gpt-6-plain"},
				{"slug": "gpt-6-api", "visibility": "list", "supported_in_api": false},
			},
		})
	}))
	defer server.Close()

	models := ListModelCatalog(context.Background(), ModelCatalogOptions{
		BaseURL:     server.URL + "/backend-api/codex",
		AccessToken: token,
		HTTPClient:  server.Client(),
	})
	for _, want := range []string{"gpt-6-terra", "gpt-6-plain", "gpt-6-api"} {
		if !contains(models, want) {
			t.Fatalf("expected OAuth model %q in catalog, got %v", want, models)
		}
	}
	for _, blocked := range []string{"gpt-6-shadow", "gpt-6-ghost"} {
		if contains(models, blocked) {
			t.Fatalf("hidden OAuth model %q must not appear, got %v", blocked, models)
		}
	}
}

func TestListModelCatalogFallsBackToCuratedOnOAuthDiscoveryFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	models := ListModelCatalog(context.Background(), ModelCatalogOptions{
		BaseURL:     server.URL + "/backend-api/codex",
		AccessToken: "oauth-token",
		HTTPClient:  server.Client(),
	})
	if len(models) != len(CuratedModels) {
		t.Fatalf("OAuth discovery failure should fall back to curated, got %v", models)
	}
	if models[0] != "gpt-6-astra" {
		t.Fatalf("fallback catalog should still lead with gpt-6-astra, got %v", models)
	}
}

func TestListModelCatalogAPIKeyModeDoesNotUseOAuthModelsEndpoint(t *testing.T) {
	oauthCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/backend-api") {
			oauthCalled = true
		}
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("client_version") != "" {
			t.Fatalf("API-key discovery must not send client_version, got %q", r.URL.Query().Get("client_version"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "gpt-5.6-codex"}},
		})
	}))
	defer server.Close()

	models := ListModelCatalog(context.Background(), ModelCatalogOptions{
		BaseURL:     server.URL + "/v1",
		APIKey:      "sk-test",
		AccessToken: "oauth-token",
		HTTPClient:  server.Client(),
	})
	if oauthCalled {
		t.Fatalf("API-key mode must not query OAuth backend")
	}
	if !contains(models, "gpt-5.6-codex") {
		t.Fatalf("expected API-key discovered model, got %v", models)
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
