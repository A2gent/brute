package http

import (
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/storage"
)

func saveRegistryIntegration(t *testing.T, s *Server, registryURL string, enabled bool) {
	t.Helper()
	now := time.Now()
	if err := s.store.SaveIntegration(&storage.Integration{
		ID: "registry", Provider: "a2_registry", Name: "A2 Registry", Mode: "duplex", Enabled: enabled,
		Config:    map[string]string{"api_key": "sq_agent", "registry_url": registryURL},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestA2gentIsFirstAndDefaultProvider(t *testing.T) {
	if got := config.SupportedProviders()[0].Type; got != config.ProviderA2gent {
		t.Fatalf("first provider = %q", got)
	}
	if got := config.DefaultConfig().ActiveProvider; got != string(config.ProviderA2gent) {
		t.Fatalf("active provider = %q", got)
	}
}

func TestA2gentRegistryKeyOnlyGoesToItsOwnRegistry(t *testing.T) {
	tests := []struct {
		name        string
		registryURL string
		enabled     bool
		baseURL     string
		want        string
	}{
		{"matching registry", "https://a2gent.net", true, "https://a2gent.net/v1", "sq_agent"},
		{"trailing slashes", "https://a2gent.net/", true, "https://a2gent.net/v1/", "sq_agent"},
		{"default registry", "", true, "https://a2gent.net/v1", "sq_agent"},
		{"other host", "https://a2gent.net", true, "https://evil.example/v1", ""},
		{"local registry with hosted endpoint", "http://localhost:9000", true, "https://a2gent.net/v1", ""},
		{"disabled integration", "https://a2gent.net", false, "https://a2gent.net/v1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newProviderTestServer(t, config.DefaultConfig())
			saveRegistryIntegration(t, server, tt.registryURL, tt.enabled)
			if got := server.a2gentRegistryAPIKey(tt.baseURL); got != tt.want {
				t.Fatalf("key = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestA2gentProviderConfiguredThroughRegistryKey(t *testing.T) {
	t.Setenv("A2GENT_API_KEY", "")
	server := newProviderTestServer(t, config.DefaultConfig())
	if server.providerConfiguredForUse(config.ProviderA2gent) {
		t.Fatal("provider must not be usable without credentials")
	}
	saveRegistryIntegration(t, server, "https://a2gent.net", true)
	if !server.providerConfiguredForUse(config.ProviderA2gent) {
		t.Fatal("registered agent must be able to use the provider")
	}
}
