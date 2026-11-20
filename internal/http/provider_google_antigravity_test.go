package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/antigravitycli"
	"github.com/A2gent/brute/internal/llm/gemini"
	"github.com/go-chi/chi/v5"
)

func putGoogleProvider(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/providers/google", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("providerType", "google")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	server.handleUpdateProvider(rec, req)
	return rec
}

func TestGoogleAuthModeRoundTrip(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AAGENT_CONFIG_PATH", configPath)
	t.Setenv("AAGENT_ANTIGRAVITY_CLI_PATH", filepath.Join(t.TempDir(), "missing-agy"))
	cfg := config.DefaultConfig()
	server := &Server{config: cfg}

	rec := putGoogleProvider(t, server, `{"auth_mode":"antigravity"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	google := cfg.Providers[string(config.ProviderGoogle)]
	if google.AuthMode != config.GoogleAuthModeAntigravity || google.Model != antigravitycli.DefaultModel {
		t.Fatalf("unexpected provider after switch: %+v", google)
	}

	var providers []ProviderConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &providers); err != nil {
		t.Fatal(err)
	}
	for _, p := range providers {
		if p.Type == string(config.ProviderGoogle) {
			if p.AuthMode != config.GoogleAuthModeAntigravity || p.Configured || p.AntigravityInstalled || p.AntigravityInstallCommand == "" {
				t.Fatalf("unexpected google response: %+v", p)
			}
		}
	}

	raw, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(raw), `"auth_mode": "antigravity"`) {
		t.Fatalf("auth_mode not persisted (err=%v):\n%s", err, raw)
	}

	rec = putGoogleProvider(t, server, `{"auth_mode":"api_key"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	google = cfg.Providers[string(config.ProviderGoogle)]
	if google.AuthMode != config.GoogleAuthModeAPIKey || google.Model != config.GetProviderDefinition(config.ProviderGoogle).DefaultModel {
		t.Fatalf("unexpected provider after switching back: %+v", google)
	}

	if rec := putGoogleProvider(t, server, `{"auth_mode":"oauth"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode status = %d", rec.Code)
	}
}

func TestGoogleProviderDispatchesByAuthMode(t *testing.T) {
	t.Setenv("A2GENT_PARENT_PROXY_URL", "")
	cfg := config.DefaultConfig()
	google := cfg.Providers[string(config.ProviderGoogle)]
	google.APIKey = "key"
	cfg.Providers[string(config.ProviderGoogle)] = google
	server := &Server{config: cfg}

	client, err := server.createBaseLLMClient(config.ProviderGoogle, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(*gemini.Client); !ok {
		t.Fatalf("api_key mode client = %T", client)
	}

	google.AuthMode = config.GoogleAuthModeAntigravity
	cfg.Providers[string(config.ProviderGoogle)] = google
	client, err = server.createBaseLLMClient(config.ProviderGoogle, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(*antigravitycli.Client); !ok {
		t.Fatalf("antigravity mode client = %T", client)
	}
	if err := server.validateProxyCLIConfinement(config.ProviderGoogle, true); err == nil {
		t.Fatal("antigravity mode must not serve isolated child relays")
	}
}
