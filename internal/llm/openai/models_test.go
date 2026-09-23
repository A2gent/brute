package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func contains(models []string, target string) bool {
	for _, model := range models {
		if model == target {
			return true
		}
	}
	return false
}

func TestListModelCatalogLeadsWithGPT6AstraWithoutCredentials(t *testing.T) {
	models := ListModelCatalog(context.Background(), ModelCatalogOptions{})
	if len(models) != len(CuratedModels) {
		t.Fatalf("expected curated list of %d, got %d", len(CuratedModels), len(models))
	}
	if models[0] != "gpt-6-astra" {
		t.Fatalf("newest OpenAI flagship should lead the catalog, got %q", models[0])
	}
	for i, want := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		if models[i] != want {
			t.Fatalf("official flagship order at %d: want %q got %q", i, want, models[i])
		}
	}
	for i, want := range CuratedModels {
		if models[i] != want {
			t.Fatalf("curated order changed at %d: want %q got %q", i, want, models[i])
		}
	}
}

func TestListModelCatalogKeepsGPT6AstraWhenLiveAPIOmitsIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("unexpected models path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-5.5"},
				{"id": "text-embedding-3-large"},
			},
		})
	}))
	defer server.Close()

	models := ListModelCatalog(context.Background(), ModelCatalogOptions{
		BaseURL:    server.URL + "/v1",
		APIKey:     "sk-test",
		HTTPClient: server.Client(),
	})

	if models[0] != "gpt-6-astra" {
		t.Fatalf("gpt-6-astra must stay selectable even when /models omits it, got %v", models)
	}
	if !contains(models, "gpt-5.5") {
		t.Fatalf("live chat model missing: %v", models)
	}
	if !contains(models, "text-embedding-3-large") {
		t.Fatalf("live API ids should still be merged after curated: %v", models)
	}
}

func TestListModelCatalogPaginatesModelsEndpoint(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		after := r.URL.Query().Get("after")
		switch after {
		case "":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":     []map[string]any{{"id": "gpt-5.5"}},
				"has_more": true,
				"last_id":  "gpt-5.5",
			})
		case "gpt-5.5":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":     []map[string]any{{"id": "only-on-page-two"}},
				"has_more": false,
				"last_id":  "only-on-page-two",
			})
		default:
			t.Errorf("unexpected after cursor %q", after)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	models := ListModelCatalog(context.Background(), ModelCatalogOptions{
		BaseURL:    server.URL + "/v1",
		APIKey:     "sk-test",
		HTTPClient: server.Client(),
	})
	if pages != 2 {
		t.Fatalf("expected 2 pages, got %d", pages)
	}
	if !contains(models, "only-on-page-two") || !contains(models, "gpt-5.5") {
		t.Fatalf("paginated live ids missing: %v", models)
	}
}

func TestListModelCatalogFallsBackToCuratedOnDiscoveryFailure(t *testing.T) {
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
	if models[0] != "gpt-6-astra" {
		t.Fatalf("fallback catalog should still lead with gpt-6-astra, got %v", models)
	}
}
