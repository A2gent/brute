package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ListModels fetches available models from Gemini API with fallback
func ListModels(apiKey, baseURL string) ([]string, error) {
	if apiKey == "" {
		// Return fallback list if no API key
		return fallbackModels(), nil
	}

	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", baseURL+"/models", nil)
	if err != nil {
		return fallbackModels(), nil
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		// If API fails, return fallback list
		return fallbackModels(), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// If not authorized or error, return fallback
		return fallbackModels(), nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fallbackModels(), nil
	}

	var modelsResp ModelsResponse
	if err := json.Unmarshal(body, &modelsResp); err != nil {
		return fallbackModels(), nil
	}

	if len(modelsResp.Data) == 0 {
		return fallbackModels(), nil
	}

	models := make([]string, 0, len(modelsResp.Data))
	for _, model := range modelsResp.Data {
		models = append(models, model.ID)
	}

	return models, nil
}

// ListModelsWithContext fetches models with context support
func ListModelsWithContext(ctx context.Context, apiKey, baseURL string) ([]string, error) {
	if apiKey == "" {
		return fallbackModels(), nil
	}

	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/models", nil)
	if err != nil {
		return fallbackModels(), nil
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fallbackModels(), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fallbackModels(), nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fallbackModels(), nil
	}

	var modelsResp ModelsResponse
	if err := json.Unmarshal(body, &modelsResp); err != nil {
		return fallbackModels(), nil
	}

	if len(modelsResp.Data) == 0 {
		return fallbackModels(), nil
	}

	models := make([]string, 0, len(modelsResp.Data))
	for _, model := range modelsResp.Data {
		models = append(models, model.ID)
	}

	return models, nil
}

// fallbackModels returns a static list of known Gemini models
func fallbackModels() []string {
	return []string{
		// Gemini 4 (latest generation)
		"gemini-4-neon",
		"gemini-4-carbon",
		"gemini-4-hydrogen",

		// Gemini 3.x
		"gemini-3.5-flash",
		"gemini-3.5-flash-lite",
		"gemini-3.1-pro-preview",
		"gemini-3.1-flash-lite",

		// Image generation (Nano Banana)
		"gemini-3.1-flash-image",
		"gemini-3-pro-image",
		"gemini-3.1-flash-lite-image",
		"gemini-2.5-flash-image",

		// Gemini 2.5 (Stable generation)
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
	}
}

// DefaultModel returns the recommended default model
func DefaultModel() string {
	return "gemini-4-hydrogen"
}

// ValidateModel checks if a model name is valid
func ValidateModel(model string) error {
	validModels := fallbackModels()
	for _, m := range validModels {
		if m == model {
			return nil
		}
	}
	return fmt.Errorf("invalid model: %s", model)
}
