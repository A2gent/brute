package anthropic

import (
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

// Three breakpoints stay below Anthropic's limit of four: tools, system, and
// the last complete input message. The moving message breakpoint also covers
// retained images and allows the next request to reuse the conversation prefix.
func (c *Client) applyPromptCaching(request *anthropicRequest, systemPrompt string) {
	if len(request.Tools) > 0 {
		request.Tools[len(request.Tools)-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}
	if system := c.transformSystemPrompt(systemPrompt); system != "" {
		request.System = []contentBlock{{Type: "text", Text: system, CacheControl: &cacheControl{Type: "ephemeral"}}}
	}
	for i := len(request.Messages) - 1; i >= 0; i-- {
		msg := &request.Messages[i]
		var blocks []contentBlock
		switch content := msg.Content.(type) {
		case string:
			if content != "" {
				blocks = []contentBlock{{Type: "text", Text: content}}
			}
		case []contentBlock:
			blocks = content
		}
		if len(blocks) > 0 {
			blocks[len(blocks)-1].CacheControl = &cacheControl{Type: "ephemeral"}
			msg.Content = blocks
			break
		}
	}
}

type anthropicUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
}

func (usage anthropicUsage) tokenUsage() llm.TokenUsage {
	logging.Debug("Anthropic prompt cache: read_tokens=%d creation_tokens=%d uncached_tokens=%d", usage.CacheReadTokens, usage.CacheCreationTokens, usage.InputTokens)
	// Anthropic's input_tokens excludes cache reads and writes. Context tracking
	// and the rest of Brute expect the total, with cache reads as a subset.
	return llm.TokenUsage{
		InputTokens:       usage.InputTokens + usage.CacheCreationTokens + usage.CacheReadTokens,
		OutputTokens:      usage.OutputTokens,
		CachedInputTokens: usage.CacheReadTokens,
	}
}
