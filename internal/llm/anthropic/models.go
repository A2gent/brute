package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const modelsAPIURL = "https://api.anthropic.com/v1/models?limit=100"

type Model struct {
	ID             string `json:"id"`
	DisplayName    string `json:"display_name"`
	CreatedAt      string `json:"created_at"`
	MaxInputTokens int    `json:"max_input_tokens"`
}

type ModelsResponse struct {
	Data []Model `json:"data"`
}

func ListModelsWithContext(ctx context.Context, apiKey string) ([]string, map[string]int) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	}
	if apiKey == "" {
		return fallbackModels(), nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	models, err := fetchModels(ctx, client, modelsAPIURL, apiKey)
	if err != nil || len(models) == 0 {
		return fallbackModels(), nil
	}

	modelIDs := make([]string, 0, len(models))
	windows := make(map[string]int, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		modelIDs = append(modelIDs, id)
		if model.MaxInputTokens > 0 {
			windows[id] = model.MaxInputTokens
		}
	}
	if len(modelIDs) == 0 {
		return fallbackModels(), nil
	}
	return modelIDs, windows
}

func fetchModels(ctx context.Context, client *http.Client, apiURL, apiKey string) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Anthropic Models API returned %s", resp.Status)
	}

	var modelsResp ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, err
	}
	return modelsResp.Data, nil
}

// ListModels returns model IDs from Anthropic's Models API.
func ListModels(apiKey string) ([]string, error) {
	models, _ := ListModelsWithContext(context.Background(), apiKey)
	return models, nil
}

// CLIModels returns the selectable model identifiers for the Claude CLI provider.
func CLIModels(apiKey string) []string {
	models, _ := ListModels(apiKey)
	ordered := append([]string{"opus", "sonnet", "haiku"}, models...)
	out := make([]string, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, model := range ordered {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

func fallbackModels() []string {
	return []string{
		"claude-fable-5",
		"claude-sonnet-5",
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-opus-4-6",
		"claude-opus-4-5",
		"claude-sonnet-4-6",
		"claude-sonnet-4-5",
		"claude-haiku-4-5",
	}
}

func DefaultModel() string {
	return "claude-opus-4-8"
}
