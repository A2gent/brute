package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestSanitizeGeneratedCommitMessagePreservesLongMessages(t *testing.T) {
	message := "refactor(reviews): centralize review states\n\n" +
		strings.Repeat("- Preserve complete technical details and regression coverage.\n", 35) +
		"- Keep the final bullet intact: проверка завершена."
	got := sanitizeGeneratedCommitMessage("Commit message:\r\n" + strings.ReplaceAll(message, "\n", "\r\n"))
	if got != message {
		t.Fatalf("commit message was altered or truncated: got %d bytes, want %d bytes", len(got), len(message))
	}
}

func TestGitCommitMessageGenerationAllowsLongResponses(t *testing.T) {
	message := "feat: preserve detailed commit messages\n\n" + strings.Repeat("- Complete change description.\n", 30)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.MaxTokens < 2048 {
			t.Errorf("commit generation token budget = %d, want at least 2048", request.MaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": message}, "finish_reason": "stop"}},
		})
	}))
	defer upstream.Close()

	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		Name: string(config.ProviderOpenAI), APIKey: "test-key", BaseURL: upstream.URL, Model: "gpt-4o",
	}
	server := &Server{config: cfg}
	response, err := server.generateGitCommitMessageWithProvider(context.Background(), config.ProviderOpenAI, "gpt-4o", "Generate a commit message")
	if err != nil {
		t.Fatalf("generate commit message: %v", err)
	}
	if response.Content != message {
		t.Fatal("generation did not preserve the complete provider response")
	}
}
