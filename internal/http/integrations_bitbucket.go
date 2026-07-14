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

func (s *Server) testBitbucketIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	if integration == nil {
		return false, "Integration is required"
	}
	email := strings.TrimSpace(integration.Config["email"])
	token := strings.TrimSpace(integration.Config["api_token"])
	if email == "" || token == "" {
		return false, "Bitbucket integration requires email and api_token"
	}
	baseURL := strings.TrimRight(strings.TrimSpace(integration.Config["api_base_url"]), "/")
	if baseURL == "" {
		baseURL = "https://api.bitbucket.org"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false, "Bitbucket api_base_url must be an absolute http or https URL"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/2.0/user", nil)
	if err != nil {
		return false, "Failed to create Bitbucket test request: " + err.Error()
	}
	req.SetBasicAuth(email, token)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return false, "Bitbucket test request failed: " + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return false, fmt.Sprintf("Bitbucket API test failed with status %d: %s", resp.StatusCode, message)
	}
	return true, "Bitbucket connection verified with /2.0/user."
}
