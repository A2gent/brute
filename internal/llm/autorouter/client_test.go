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
	if router.request.SystemPrompt != automaticRouterSystemPrompt {
		t.Fatal("router did not receive the hardened system prompt as provider instructions")
	}
	if len(router.request.Messages) != 1 || router.request.Messages[0].Role != "user" {
		t.Fatalf("router messages = %#v, want one user message and no system message", router.request.Messages)
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

func TestValidateAutoRouterProviderAcceptsCustomClaudeRefs(t *testing.T) {
	customRef := "anthropic:work"
	err := validateAutoRouterProvider(config.Provider{
		RouterProvider: customRef,
		RouterRules: []config.RouterRule{
			{Match: "coding", Provider: customRef, Model: "claude-sonnet-4-6"},
		},
	})
	if err != nil {
		t.Fatalf("validateAutoRouterProvider(custom Claude refs): %v", err)
	}
}

func TestValidateAutoRouterProviderRejectsJevAsTarget(t *testing.T) {
	err := validateAutoRouterProvider(config.Provider{
		RouterProvider: string(config.ProviderJev),
		RouterRules: []config.RouterRule{
			{Match: "coding", Provider: string(config.ProviderJev), Model: "jev-latest"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "classifier") {
		t.Fatalf("error = %v, want classifier target rejection", err)
	}
}

func TestProvenanceOnNonStreamingRoutedTarget(t *testing.T) {
	cfg := routerTestConfig()
	provider := cfg.Providers[string(config.ProviderAutoRouter)]
	provider.RouterRules = []config.RouterRule{{Match: "all", Provider: "anthropic:work", Model: "model-a"}}
	cfg.Providers[string(config.ProviderAutoRouter)] = provider
	client := New(cfg, func(ref, model string) (llm.Client, string, error) {
		return &routerTestClient{response: &llm.ChatResponse{Content: "answer"}}, model, nil
	})
	seen := false
	response, err := client.ChatStream(context.Background(), &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hi"}}}, func(ev llm.StreamEvent) error {
		if ev.Type == llm.StreamEventContentDelta {
			seen = true
			if ev.Provenance == nil || ev.Provenance.Provider != "anthropic:work" || ev.Provenance.Model != "model-a" {
				t.Fatalf("delta = %+v", ev)
			}
		}
		return nil
	})
	if err != nil || !seen || response.Provenance == nil {
		t.Fatalf("response=%+v err=%v seen=%v", response, err, seen)
	}
}

func TestRouterPromptIsCappedAndImagesGoOnlyToJev(t *testing.T) {
	long := "goal: " + strings.Repeat("контекст ", 20_000) + " final ask"
	images := []llm.Image{{Name: "shot.png", DataBase64: "AAAA"}}
	for _, routerProvider := range []string{string(config.ProviderLMStudio), string(config.ProviderJev)} {
		cfg := routerTestConfig()
		provider := cfg.Providers[string(config.ProviderAutoRouter)]
		provider.RouterProvider = routerProvider
		cfg.Providers[string(config.ProviderAutoRouter)] = provider
		router := &routerTestClient{response: &llm.ChatResponse{Content: `{"index":2}`}}
		client := New(cfg, func(ref, model string) (llm.Client, string, error) {
			if ref == routerProvider {
				return router, model, nil
			}
			return &routerTestClient{}, model, nil
		})
		if _, _, err := client.resolveTarget(context.Background(), &llm.ChatRequest{
			Messages: []llm.Message{{Role: "user", Content: long, Images: images}},
		}); err != nil {
			t.Fatalf("%s: resolveTarget: %v", routerProvider, err)
		}
		msg := router.request.Messages[0]
		if len(msg.Content) > routerPromptMaxBytes+1024 || !strings.Contains(msg.Content, "goal:") || !strings.HasSuffix(msg.Content, "final ask") {
			t.Fatalf("%s: router prompt not capped with head and tail kept (%d bytes)", routerProvider, len(msg.Content))
		}
		if wantImages := routerProvider == string(config.ProviderJev); (len(msg.Images) > 0) != wantImages {
			t.Fatalf("%s: images forwarded = %d, want %v", routerProvider, len(msg.Images), wantImages)
		}
	}
}
