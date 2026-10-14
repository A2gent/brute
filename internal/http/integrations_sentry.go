package http

import (
	"context"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

// testSentryIntegration verifies the API token and organization with a read-only project listing.
func (s *Server) testSentryIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	if integration == nil {
		return false, "Integration is required"
	}
	if err := integrationtools.ProbeSentry(ctx, integration); err != nil {
		return false, err.Error()
	}
	return true, "Sentry connection verified: token and organization are valid."
}
