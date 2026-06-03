package http

// Leonardo-specific DTOs and handlers stay separate from generic integration CRUD.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type LeonardoModelsRequest struct {
	APIKey string `json:"api_key"`
}

type LeonardoModelOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type LeonardoModelsResponse struct {
	Models []LeonardoModelOption `json:"models"`
}

func (s *Server) handleListLeonardoModels(w http.ResponseWriter, r *http.Request) {
	var req LeonardoModelsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		s.errorResponse(w, http.StatusBadRequest, "api_key is required")
		return
	}

	models, err := listLeonardoPlatformModels(r.Context(), apiKey)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to load Leonardo models: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, LeonardoModelsResponse{Models: models})
}

func listLeonardoPlatformModels(ctx context.Context, apiKey string) ([]LeonardoModelOption, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloud.leonardo.ai/api/rest/v1/platformModels", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build Leonardo models request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	request.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read Leonardo response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = response.Status
		}
		return nil, fmt.Errorf("status %d: %s", response.StatusCode, detail)
	}

	var payload interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failed to decode Leonardo models response: %w", err)
	}

	models := collectLeonardoModels(payload)
	if len(models) == 0 {
		return nil, fmt.Errorf("Leonardo returned no platform models")
	}
	sort.Slice(models, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(models[i].Name))
		right := strings.ToLower(strings.TrimSpace(models[j].Name))
		if left == right {
			return models[i].ID < models[j].ID
		}
		return left < right
	})
	return models, nil
}

func collectLeonardoModels(payload interface{}) []LeonardoModelOption {
	seen := make(map[string]struct{})
	out := make([]LeonardoModelOption, 0)

	var visit func(value interface{})
	visit = func(value interface{}) {
		switch typed := value.(type) {
		case map[string]interface{}:
			id := firstNonEmptyString(typed, "id", "modelId", "model_id")
			name := firstNonEmptyString(typed, "name", "modelName", "model_name")
			if id != "" && name != "" {
				if _, exists := seen[id]; !exists {
					seen[id] = struct{}{}
					out = append(out, LeonardoModelOption{
						ID:          id,
						Name:        name,
						Description: firstNonEmptyString(typed, "description"),
					})
				}
			}
			for _, child := range typed {
				visit(child)
			}
		case []interface{}:
			for _, item := range typed {
				visit(item)
			}
		}
	}

	visit(payload)
	return out
}

func firstNonEmptyString(record map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		raw, ok := record[key]
		if !ok {
			continue
		}
		value, ok := raw.(string)
		if ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
