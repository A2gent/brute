package anthropic

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Model represents an Anthropic model
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// ModelsResponse represents the response from Anthropic models API
type ModelsResponse struct {
	Data []Model `json:"data"`
}

// ListModels fetches the current model catalog from the official Anthropic
// Models API (GET /v1/models) so the selectable list always reflects what the
// account can actually use. apiKey is optional; when empty, the
// ANTHROPIC_API_KEY environment variable is used. If no key is available or the
// request fails for any reason, a curated fallback of known-current models is
// returned instead.
func ListModels(apiKey string) ([]string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	}
	if apiKey == "" {
		// Return fallback list if no API key
		return fallbackModels(), nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", "https://api.anthropic.com/v1/models?limit=100", nil)
	if err != nil {
		return fallbackModels(), nil
	}

	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

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
		id := strings.TrimSpace(model.ID)
		if id != "" {
			models = append(models, id)
		}
	}

	if len(models) == 0 {
		return fallbackModels(), nil
	}

	return models, nil
}

// CLIModels returns the selectable model identifiers for the Claude CLI
// provider: the CLI shorthand aliases (which Claude Code resolves to the current
// default of each tier) followed by the live or fallback concrete model IDs from
// ListModels, de-duplicated with order preserved. apiKey is optional; when empty
// the ANTHROPIC_API_KEY environment variable is used.
func CLIModels(apiKey string) []string {
	models, _ := ListModels(apiKey)

	ordered := append([]string{"opus", "sonnet", "haiku"}, models...)
	out := make([]string, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, m := range ordered {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}

// fallbackModels returns a curated list of currently-available models, used when
// the live Models API cannot be reached. Newest / most-capable first.
func fallbackModels() []string {
	return []string{
		// Claude 5 family (most capable)
		"claude-fable-5",
		"claude-sonnet-5",

		// Claude Opus 4.x
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-opus-4-6",
		"claude-opus-4-5",

		// Claude Sonnet 4.x
		"claude-sonnet-4-6",
		"claude-sonnet-4-5",

		// Claude Haiku
		"claude-haiku-4-5",
	}
}

// DefaultModel returns the recommended default model
func DefaultModel() string {
	return "claude-opus-4-8"
}
