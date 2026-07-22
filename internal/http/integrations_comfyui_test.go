package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestIntegrationCreateAcceptsComfyUIProvider(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	body, err := json.Marshal(IntegrationRequest{
		Provider: "comfyui",
		Mode:     "notify_only",
		Config: map[string]string{
			"base_url":   "http://127.0.0.1:8188",
			"checkpoint": "demo.safetensors",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/integrations", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateIntegration(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp IntegrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Provider != "comfyui" {
		t.Fatalf("expected provider comfyui, got %q", resp.Provider)
	}
	if resp.Name != "ComfyUI" {
		t.Fatalf("expected default name ComfyUI, got %q", resp.Name)
	}
	if got := resp.Config["base_url"]; got != "http://127.0.0.1:8188" {
		t.Fatalf("expected base_url preserved, got %q", got)
	}
}

func TestIntegrationCreateRejectsComfyUIWithoutHTTPBaseURL(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	body, err := json.Marshal(IntegrationRequest{
		Provider: "comfyui",
		Mode:     "notify_only",
		Config: map[string]string{
			"base_url": "127.0.0.1:8188",
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/integrations", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateIntegration(rec, req)
	if rec.Code == http.StatusCreated {
		t.Fatalf("expected validation failure, got created: %s", rec.Body.String())
	}
}

func TestTestComfyUIIntegrationReachable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/system_stats" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"system":{"os":"darwin"}}`))
	}))
	defer upstream.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	if err := store.SaveIntegration(&storage.Integration{
		ID:        "comfy-test",
		Provider:  "comfyui",
		Name:      "ComfyUI",
		Mode:      "notify_only",
		Enabled:   true,
		Config:    map[string]string{"base_url": upstream.URL},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("failed to save integration: %v", err)
	}

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)
	req := httptest.NewRequest(http.MethodPost, "/integrations/comfy-test/test", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp IntegrationTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got %#v", resp)
	}
}
