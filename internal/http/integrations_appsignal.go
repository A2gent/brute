package http

import (
	"context"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

// testAppSignalIntegration verifies the MCP token by listing available tools.
// The probe is read-only and does not call any AppSignal mutation tool.
func (s *Server) testAppSignalIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	if integration == nil {
		return false, "Integration is required"
	}
	apiKey := strings.TrimSpace(integration.Config["api_key"])
	if apiKey == "" {
		return false, "AppSignal integration requires api_key"
	}

	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tool := integrationtools.NewAppSignalQueryTool(s.store)
	if err := tool.TestConnection(probeCtx, integration); err != nil {
		return false, "AppSignal test request failed: " + err.Error()
	}
	return true, "AppSignal connection verified with read-only MCP tools/list."
}
