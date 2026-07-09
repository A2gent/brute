package autorouter

import (
	"context"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
)

type routerTestClient struct {
	response *llm.ChatResponse
	request  *llm.ChatRequest
}

func (c *routerTestClient) Chat(_ context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	c.request = request
	return c.response, nil
}

func routerTestConfig() *config.Config {
	return &config.Config{
		Providers: map[string]config.Provider{
			string(config.ProviderAutoRouter): {
				Name:           string(config.ProviderAutoRouter),
				RouterProvider: string(config.ProviderLMStudio),
				RouterModel:    "router-model",
				RouterRules: []config.RouterRule{
					{Match: "documentation", Provider: string(config.ProviderGoogle), Model: "docs-model"},
					{Match: "coding, mid complexity", Provider: string(config.ProviderCursor), Model: "coding-model"},
				},
			},
		},
	}
}

func TestResolveTargetFailsOnTruncatedRouterResponse(t *testing.T) {
	router := &routerTestClient{response: &llm.ChatResponse{Content: `{"index":`}}
	client := New(routerTestConfig(), func(providerRef string, model string) (llm.Client, string, error) {
		if providerRef != string(config.ProviderLMStudio) {
			t.Fatalf("unexpected target client creation after routing failure: %s/%s", providerRef, model)
		}
		return router, model, nil
	})

	_, _, err := client.resolveTarget(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "Create a controller concern and run its specs."}},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid router response") {
		t.Fatalf("error = %v, want invalid router response", err)
	}
	if router.request == nil {
		t.Fatal("router request was not captured")
	}
	if router.request.MaxTokens != 2048 {
		t.Fatalf("router max tokens = %d, want 2048", router.request.MaxTokens)
	}
	if router.request.Messages[0].Content != automaticRouterSystemPrompt {
		t.Fatal("router did not receive the hardened system prompt")
	}
}

func TestResolveTargetFailsOnOutOfRangeRouterIndex(t *testing.T) {
	router := &routerTestClient{response: &llm.ChatResponse{Content: `{"index":0,"reason":"none"}`}}
	client := New(routerTestConfig(), func(providerRef string, model string) (llm.Client, string, error) {
		if providerRef != string(config.ProviderLMStudio) {
			t.Fatalf("unexpected target client creation after routing failure: %s/%s", providerRef, model)
		}
		return router, model, nil
	})

	_, _, err := client.resolveTarget(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "Implement the source change."}},
	})
	if err == nil || !strings.Contains(err.Error(), "out-of-range index 0") {
		t.Fatalf("error = %v, want out-of-range routing error", err)
	}
}
