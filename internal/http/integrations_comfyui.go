package http

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func (s *Server) testComfyUIIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	baseURL := strings.TrimRight(strings.TrimSpace(integration.Config["base_url"]), "/")
	if baseURL == "" {
		return false, "missing required config field: base_url"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/system_stats", nil)
	if err != nil {
		return false, fmt.Sprintf("failed to build ComfyUI request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey := strings.TrimSpace(integration.Config["api_key"]); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Sprintf("ComfyUI is unreachable at %s: %v", baseURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = resp.Status
		}
		return false, fmt.Sprintf("ComfyUI responded with status %d: %s", resp.StatusCode, detail)
	}
	return true, fmt.Sprintf("Connected to ComfyUI at %s", baseURL)
}
