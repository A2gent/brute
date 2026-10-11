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

// testModel3DIntegration checks the Meshy/Tripo API key by reading the credit
// balance, which is free and proves both auth and connectivity.
func (s *Server) testModel3DIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	apiKey := strings.TrimSpace(integration.Config["api_key"])
	if apiKey == "" {
		return false, "api_key is required"
	}
	baseURL := strings.TrimRight(strings.TrimSpace(integration.Config["base_url"]), "/")
	var endpoint, label string
	switch integration.Provider {
	case "meshy":
		if baseURL == "" {
			baseURL = "https://api.meshy.ai"
		}
		endpoint, label = baseURL+"/openapi/v1/balance", "Meshy"
	case "tripo":
		if baseURL == "" {
			baseURL = "https://openapi.tripo3d.ai/v3"
		}
		endpoint, label = baseURL+"/account/balance", "Tripo3D"
	default:
		return false, "unsupported 3D provider: " + integration.Provider
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Sprintf("%s request failed: %v", label, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Sprintf("%s API returned status %d: %s", label, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Meshy returns {"balance": n}; Tripo wraps it as {"code":0,"data":{"balance": n}}.
	var parsed struct {
		Code    int      `json:"code"`
		Balance *float64 `json:"balance"`
		Data    struct {
			Balance *float64 `json:"balance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, fmt.Sprintf("%s returned an unexpected response", label)
	}
	if parsed.Code != 0 {
		return false, fmt.Sprintf("%s API error code %d", label, parsed.Code)
	}
	balance := parsed.Balance
	if balance == nil {
		balance = parsed.Data.Balance
	}
	if balance == nil {
		return true, fmt.Sprintf("Connected to %s", label)
	}
	return true, fmt.Sprintf("Connected to %s (credit balance: %g)", label, *balance)
}
