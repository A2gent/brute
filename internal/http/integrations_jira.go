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

// testJiraIntegration performs a read-only connectivity check against Jira Cloud.
func (s *Server) testJiraIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	if integration == nil {
		return false, "Integration is required"
	}
	baseURL := strings.TrimRight(strings.TrimSpace(integration.Config["base_url"]), "/")
	email := strings.TrimSpace(integration.Config["email"])
	apiToken := strings.TrimSpace(integration.Config["api_token"])
	if baseURL == "" || email == "" || apiToken == "" {
		return false, "Jira integration requires base_url, email, and api_token"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false, "Jira base_url must be an absolute URL"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/rest/api/3/myself", nil)
	if err != nil {
		return false, "Failed to create Jira test request: " + err.Error()
	}
	req.SetBasicAuth(email, apiToken)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, "Jira test request failed: " + err.Error()
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return false, fmt.Sprintf("Jira API test failed with status %d: %s", resp.StatusCode, msg)
	}
	return true, "Jira connection verified with read-only /rest/api/3/myself."
}
