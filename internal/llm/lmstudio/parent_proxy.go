package lmstudio

import (
	"encoding/json"
	"strings"

	"github.com/A2gent/brute/internal/llm"
)

// NewParentProxyClient opts into Brute's private Chat Completions extensions.
// Ordinary OpenAI-compatible endpoints must not receive opaque reasoning state.
func NewParentProxyClient(apiKey, model, baseURL string) *Client {
	client := NewClient(apiKey, model, baseURL)
	client.parentProxy = true
	return client
}

func (c *Client) applyRequestOptions(body *openAIRequest, request *llm.ChatRequest) {
	if c.providerName() == "OpenAI" || c.parentProxy {
		body.PromptCacheKey = strings.TrimSpace(request.PromptCacheKey)
		if body.PromptCacheKey == "" {
			body.PromptCacheKey = strings.TrimSpace(request.SessionID)
		}
	}
	if !c.parentProxy {
		return
	}
	body.SessionID = strings.TrimSpace(request.SessionID)
	body.ReasoningEffort = strings.TrimSpace(request.ReasoningEffort)
	body.TextVerbosity = strings.TrimSpace(request.TextVerbosity)
	body.ServiceTier = strings.TrimSpace(request.ServiceTier)
	if body.SessionID != "" {
		body.PromptCacheKey = body.SessionID
	}
	if strings.HasSuffix(strings.TrimRight(c.baseURL, "/"), "/providers/openai") {
		// The parent handles Responses token caps. Never add LM Studio's default
		// reasoning budget, or send a model-specific field the proxy cannot read.
		body.MaxTokens = request.MaxTokens
		body.MaxCompletionTokens = 0
	}
}

func (c *Client) proxyReasoningItems(message llm.Message) []json.RawMessage {
	if c.parentProxy && message.Role == "assistant" {
		return message.ReasoningItems
	}
	return nil
}
