package http

import (
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestValidateIntegrationA2ATransport(t *testing.T) {
	t.Parallel()

	base := storage.Integration{
		Provider: "a2_registry",
		Mode:     "duplex",
		Config: map[string]string{
			"api_key": "sq_key",
		},
	}

	t.Run("grpc requires grpc address", func(t *testing.T) {
		integration := base
		integration.Config = map[string]string{
			"api_key":   "sq_key",
			"transport": "grpc",
		}
		err := validateIntegration(integration)
		if err == nil {
			t.Fatal("expected validation error")
		}
	})

	t.Run("grpc valid", func(t *testing.T) {
		integration := base
		integration.Config = map[string]string{
			"api_key":          "sq_key",
			"transport":        "grpc",
			"square_grpc_addr": "localhost:9001",
		}
		if err := validateIntegration(integration); err != nil {
			t.Fatalf("expected valid integration, got %v", err)
		}
	})

	t.Run("websocket requires ws url", func(t *testing.T) {
		integration := base
		integration.Config = map[string]string{
			"api_key":   "sq_key",
			"transport": "websocket",
		}
		err := validateIntegration(integration)
		if err == nil {
			t.Fatal("expected validation error")
		}
	})

	t.Run("websocket valid", func(t *testing.T) {
		integration := base
		integration.Config = map[string]string{
			"api_key":       "sq_key",
			"transport":     "websocket",
			"square_ws_url": "ws://localhost:9000/tunnel/ws",
		}
		if err := validateIntegration(integration); err != nil {
			t.Fatalf("expected valid integration, got %v", err)
		}
	})
}
