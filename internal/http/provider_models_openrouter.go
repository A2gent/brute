package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
)

const (
	openRouterModelsURL            = "https://openrouter.ai/api/v1/models"
	openRouterModelsRequestTimeout = 10 * time.Second
)

type openRouterModelsHTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type openRouterModelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		ContextLength int    `json:"context_length"`
	} `json:"data"`
}

func (s *Server) handleListOpenRouterModels(w http.ResponseWriter, r *http.Request) {
	provider := s.config.Providers[string(config.ProviderOpenRouter)]
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		apiKey = s.apiKeyFromEnv(config.ProviderOpenRouter)
	}
	if apiKey == "" {
		s.errorResponse(w, http.StatusBadRequest, "OpenRouter API key is not configured")
		return
	}

	models, err := fetchOpenRouterModels(r.Context(), s.openRouterModelsClient, apiKey)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to fetch models from OpenRouter: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, ListProviderModelsResponse{Models: models})
}

func fetchOpenRouterModels(ctx context.Context, client openRouterModelsHTTPClient, apiKey string) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	requestCtx, cancel := context.WithTimeout(ctx, openRouterModelsRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, openRouterModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to OpenRouter: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read OpenRouter models response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OpenRouter returned error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload openRouterModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse OpenRouter models response: %w", err)
	}

	models := make([]string, 0, len(payload.Data))
	contextCache := make(map[string]int, len(payload.Data))
	for _, model := range payload.Data {
		if id := strings.TrimSpace(model.ID); id != "" {
			models = append(models, id)
			if model.ContextLength > 0 {
				contextCache[id] = model.ContextLength
			}
		}
	}
	config.CacheOpenRouterModelContextWindows(contextCache)
	sort.Strings(models)
	return models, nil
}
