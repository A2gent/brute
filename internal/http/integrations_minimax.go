package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// MiniMax has no free balance endpoint, so the key is checked by querying a
// task that cannot exist: a valid key gets a "not found" answer, a bad key
// gets an authorization error.
func (s *Server) testMiniMaxIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	apiKey := strings.TrimSpace(integration.Config["api_key"])
	if apiKey == "" {
		return false, "api_key is required"
	}
	baseURL := strings.TrimRight(strings.TrimSpace(integration.Config["base_url"]), "/")
	if baseURL == "" {
		baseURL = "https://api.minimax.io"
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/query/video_generation?task_id=0", nil)
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Sprintf("MiniMax request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, fmt.Sprintf("MiniMax rejected the API key (status %d)", resp.StatusCode)
	}
	var parsed struct {
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, "MiniMax returned an unexpected response"
	}
	switch parsed.BaseResp.StatusCode {
	case 1004, 2049:
		return false, fmt.Sprintf("MiniMax rejected the API key: %s", strings.TrimSpace(parsed.BaseResp.StatusMsg))
	}
	if resp.StatusCode >= 500 {
		return false, fmt.Sprintf("MiniMax API returned status %d", resp.StatusCode)
	}
	return true, "Connected to MiniMax (API key accepted)"
}
