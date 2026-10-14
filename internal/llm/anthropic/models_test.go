package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicModelsAPIContextWindows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" || r.URL.Query().Get("limit") != "100" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Fatalf("x-api-key = %q, want test-key", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version = %q, want 2023-06-01", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-5-5","display_name":"Claude Opus 5.5","max_input_tokens":1000000,"max_tokens":128000},{"id":"claude-haiku-4-5","max_input_tokens":200000}]}`))
	}))
	defer server.Close()

	models, err := fetchModels(context.Background(), server.Client(), server.URL+"/v1/models?limit=100", "test-key")
	if err != nil {
		t.Fatalf("fetchModels returned error: %v", err)
	}
	if len(models) != 2 || models[0].ID != "claude-opus-5-5" || models[0].MaxInputTokens != 1000000 || models[1].MaxInputTokens != 200000 {
		t.Fatalf("models = %+v", models)
	}
}

func TestListModelsFallsBackWhenAPIKeyMissing(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	models, windows := ListModelsWithContext(context.Background(), "")
	if len(models) == 0 {
		t.Fatal("expected fallback models without API key")
	}
	if windows != nil {
		t.Fatalf("fallback windows = %+v, want nil", windows)
	}
}
