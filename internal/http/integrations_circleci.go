package http

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// testCircleCIIntegration performs a read-only connectivity check against CircleCI v2.
func (s *Server) testCircleCIIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	if integration == nil {
		return false, "Integration is required"
	}
	apiToken := strings.TrimSpace(integration.Config["api_token"])
	if apiToken == "" {
		return false, "CircleCI integration requires api_token"
	}
	baseURL := strings.TrimRight(strings.TrimSpace(integration.Config["api_base_url"]), "/")
	if baseURL == "" {
		baseURL = "https://circleci.com"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false, "CircleCI api_base_url must be an absolute URL"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v2/me", nil)
	if err != nil {
		return false, "Failed to create CircleCI test request: " + err.Error()
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Circle-Token", apiToken)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, "CircleCI test request failed: " + err.Error()
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return false, fmt.Sprintf("CircleCI API test failed with status %d: %s", resp.StatusCode, msg)
	}
	return true, "CircleCI connection verified with read-only /api/v2/me."
}
