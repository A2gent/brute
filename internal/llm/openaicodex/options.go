package openaicodex

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

func codexInstructions(systemPrompt string) string {
	prompt := strings.TrimSpace(systemPrompt)
	if prompt == "" {
		return codexPrefix
	}
	return codexPrefix + "\n\n" + prompt
}

func normalizeOptions(options Options) Options {
	options.PromptCacheKey = strings.TrimSpace(options.PromptCacheKey)
	options.ReasoningEffort = strings.TrimSpace(options.ReasoningEffort)
	options.TextVerbosity = strings.TrimSpace(options.TextVerbosity)
	options.ServiceTier = strings.TrimSpace(options.ServiceTier)
	if !options.PublicAPI {
		options.ServiceTier = normalizeCodexServiceTier(options.ServiceTier)
		// The ChatGPT Codex backend rejects stored responses and token caps.
		options.StatefulResponses = false
		options.MaxTokens = 0
	}
	if options.MaxTokens < 0 {
		options.MaxTokens = 0
	}
	if options.ResponseHeaderTimeout <= 0 {
		options.ResponseHeaderTimeout = envDurationSeconds(
			"AAGENT_OPENAI_CODEX_RESPONSE_HEADER_TIMEOUT_SECONDS",
			defaultResponseHeaderTimeout,
		)
	}
	return options
}

func normalizeCodexServiceTier(serviceTier string) string {
	serviceTier = strings.TrimSpace(serviceTier)
	// The ChatGPT Codex backend currently rejects `service_tier: "auto"`.
	// Treat it like the default/unspecified tier and omit the field from requests.
	if strings.EqualFold(serviceTier, "auto") {
		return ""
	}
	return serviceTier
}

func (c *Client) requestOptions(request *llm.ChatRequest) Options {
	options := c.options
	if request == nil {
		return options
	}
	if strings.TrimSpace(request.PromptCacheKey) != "" {
		options.PromptCacheKey = strings.TrimSpace(request.PromptCacheKey)
	}
	if strings.TrimSpace(request.ReasoningEffort) != "" {
		options.ReasoningEffort = strings.TrimSpace(request.ReasoningEffort)
	}
	if strings.TrimSpace(request.TextVerbosity) != "" {
		options.TextVerbosity = strings.TrimSpace(request.TextVerbosity)
	}
	if strings.TrimSpace(request.ServiceTier) != "" {
		options.ServiceTier = strings.TrimSpace(request.ServiceTier)
	}
	if request.MaxTokens > 0 {
		options.MaxTokens = request.MaxTokens
	}
	return normalizeOptions(options)
}

func (options Options) promptCacheKey(request *llm.ChatRequest) string {
	if key := strings.TrimSpace(options.PromptCacheKey); key != "" {
		return key
	}
	if request != nil {
		return strings.TrimSpace(request.SessionID)
	}
	return ""
}

func (options Options) previousResponseID(request *llm.ChatRequest) string {
	if !options.StatefulResponses || request == nil {
		return ""
	}
	return strings.TrimSpace(request.PreviousResponseID)
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt(key string) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func envDurationSeconds(key string, fallback time.Duration) time.Duration {
	value := envInt(key)
	if value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Second
}
