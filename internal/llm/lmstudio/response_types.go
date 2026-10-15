package lmstudio

import (
	"encoding/json"

	"github.com/A2gent/brute/internal/llm"
)

type openAIResponse struct {
	Provenance *llm.Provenance `json:"provenance,omitempty"`
	ID         string          `json:"id"`
	Object     string          `json:"object"`
	Created    int64           `json:"created"`
	Model      string          `json:"model"`
	Choices    []struct {
		Index        int           `json:"index"`
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
		Error        *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error,omitempty"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

type openAIStreamResponse struct {
	Model      string          `json:"model"`
	Provenance *llm.Provenance `json:"provenance,omitempty"`
	Choices    []struct {
		Index int `json:"index"`
		Delta struct {
			ReasoningItems []json.RawMessage `json:"reasoning_items,omitempty"`
			Content        string            `json:"content"`
			Refusal        string            `json:"refusal,omitempty"`
			ToolCalls      []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name             string `json:"name"`
					Arguments        string `json:"arguments"`
					ThoughtSignature string `json:"thought_signature,omitempty"`
				} `json:"function"`
				ExtraContent *openAIExtraContent `json:"extra_content,omitempty"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
		Error        *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error,omitempty"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}
