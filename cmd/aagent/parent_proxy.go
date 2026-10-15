package main

import (
	"os"
	"strings"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/lmstudio"
)

func parentProxyOpenAIClient(model string) (llm.Client, bool) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")), "/")
	if baseURL == "" {
		return nil, false
	}
	apiKey := strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_KEY"))
	if apiKey == "" {
		apiKey = "a2gent-proxy"
	}
	return lmstudio.NewParentProxyClient(apiKey, model, baseURL+"/providers/openai"), true
}
