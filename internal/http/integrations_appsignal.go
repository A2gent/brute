package http

import (
	"context"
	"fmt"
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
	result, err := tool.Execute(probeCtx, []byte(fmt.Sprintf(`{"operation":"list_tools","integration_id":%q}`, integration.ID)))
	if err != nil {
		return false, "AppSignal test request failed: " + err.Error()
	}
	if result == nil || !result.Success {
		message := "AppSignal MCP test request failed"
		if result != nil && strings.TrimSpace(result.Error) != "" {
			message = result.Error
		}
		return false, message
	}
	return true, "AppSignal connection verified with read-only MCP tools/list."
}
