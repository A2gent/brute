package http

import (
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestValidateIntegrationTavilyRequiresAPIKey(t *testing.T) {
	t.Parallel()

	integration := storage.Integration{
		Provider: "tavily",
		Mode:     "notify_only",
		Config:   map[string]string{},
	}
	if err := validateIntegration(integration); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestNewIntegrationFromRequestDefaultsTavilyName(t *testing.T) {
	t.Parallel()

	integration, err := newIntegrationFromRequest(IntegrationRequest{
		Provider: "tavily",
		Mode:     "notify_only",
		Config: map[string]string{
			"api_key": "tvly-key",
		},
	})
	if err != nil {
		t.Fatalf("expected valid integration, got %v", err)
	}
	if integration.Name != "Tavily" {
		t.Fatalf("expected default name Tavily, got %q", integration.Name)
	}
}
