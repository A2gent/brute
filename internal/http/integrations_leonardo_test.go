package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestHandleListLeonardoModels_UsesV2Endpoint(t *testing.T) {
	// This test changes a package-global endpoint, so it must not run in parallel.
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
			t.Fatalf("expected Bearer auth, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"phoenix","name":"Phoenix"},{"id":"kino-xl","name":"Leonardo Kino XL"}]}`))
	})
	mock := httptest.NewServer(mux)
	t.Cleanup(mock.Close)

	orig := leonardoModelsBaseURL
	leonardoModelsBaseURL = mock.URL
	t.Cleanup(func() { leonardoModelsBaseURL = orig })

	models, err := listLeonardoPlatformModels(t.Context(), "test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "kino-xl" {
		t.Fatalf("unexpected model id: %q", models[0].ID)
	}
}

func TestHandleListLeonardoModels_MissingAPIKey(t *testing.T) {
	t.Parallel()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	body, _ := json.Marshal(LeonardoModelsRequest{APIKey: ""})
	req := httptest.NewRequest(http.MethodPost, "/integrations/leonardo/models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleListLeonardoModels(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty api_key, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCollectLeonardoModels_V2DataArray(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"data":[{"id":"phoenix","name":"Phoenix"},{"id":"kino-xl","name":"Leonardo Kino XL"}]}`)
	var payload interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	models := collectLeonardoModels(payload)
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "phoenix" || models[0].Name != "Phoenix" {
		t.Fatalf("unexpected first model: %+v", models[0])
	}
}

// TestHandleListLeonardoModels_IntegrationIDResolvesKey verifies that when
// integration_id is sent instead of api_key, the handler fetches the real
// (unmasked) key from the store and proxies it to Leonardo.
func TestHandleListLeonardoModels_IntegrationIDResolvesKey(t *testing.T) {
	// This test changes a package-global endpoint, so it must not run in parallel.
	// Mock Leonardo API that verifies the real key is received.
	var receivedAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"phoenix","name":"Phoenix"}]}`))
	})
	mock := httptest.NewServer(mux)
	t.Cleanup(mock.Close)

	orig := leonardoModelsBaseURL
	leonardoModelsBaseURL = mock.URL
	t.Cleanup(func() { leonardoModelsBaseURL = orig })

	// Create a store with a saved Leonardo integration containing the real key.
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	realKey := "leonardo-real-api-key-12345"
	now := time.Now().UTC()
	if err := store.SaveIntegration(&storage.Integration{
		ID:        "leo-1",
		Provider:  "leonardo",
		Name:      "Test Leonardo",
		Mode:      "notify_only",
		Enabled:   true,
		Config:    map[string]string{"api_key": realKey},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("failed to save integration: %v", err)
	}

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	// Send integration_id instead of api_key (simulates the masked "***" scenario).
	body, _ := json.Marshal(LeonardoModelsRequest{IntegrationID: "leo-1"})
	req := httptest.NewRequest(http.MethodPost, "/integrations/leonardo/models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleListLeonardoModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if receivedAuth != "Bearer "+realKey {
		t.Fatalf("expected real key in auth header, got %q", receivedAuth)
	}
}
