package http

import (
	"context"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

// testCloudflareIntegration verifies the API token and reports the account and domains it can see.
func (s *Server) testCloudflareIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	if integration == nil {
		return false, "Integration is required"
	}
	message, err := integrationtools.ProbeCloudflare(ctx, integration)
	if err != nil {
		return false, err.Error()
	}
	return true, message
}
