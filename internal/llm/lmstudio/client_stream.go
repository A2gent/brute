package lmstudio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

// ChatStream sends a streaming chat request to LM Studio.
func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	headers, cleanup, err := c.prepareRequest(ctx, request)
	defer cleanup()
	if err != nil {
		return nil, err
	}

	model := request.Model
	if model == "" {
		model = c.model
	}

	maxTokens := c.effectiveMaxTokens(request.MaxTokens)

	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	messages := make([]openAIMessage, 0, len(request.Messages)+1)
	if request.SystemPrompt != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: request.SystemPrompt})
	}
	for _, msg := range request.Messages {
		messages = append(messages, c.convertMessage(msg)...)
	}

	var tools []openAITool
	for _, t := range request.Tools {
		tools = append(tools, openAITool{
			Type: "function",
			Function: struct {
				Name        string                 `json:"name"`
				Description string                 `json:"description"`
				Parameters  map[string]interface{} `json:"parameters"`
			}{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	reqBody := openAIRequest{
		Model:    model,
		Messages: messages,
		Tools:    tools,
		Stream:   true,
		// Usage arrives in a final chunk with empty choices only when requested.
		StreamOptions: &struct {
			IncludeUsage bool `json:"include_usage"`
		}{IncludeUsage: true},
	}
	if c.providerName() == "OpenAI" {
		reqBody.PromptCacheKey = strings.TrimSpace(request.PromptCacheKey)
		if reqBody.PromptCacheKey == "" {
			reqBody.PromptCacheKey = strings.TrimSpace(request.SessionID)
		}
	}
	applyTokenLimit(&reqBody, model, maxTokens, request.Temperature)

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	mergeRequestHeaders(httpReq.Header, headers)
	resp, err := llm.DoStream(c.httpClient, httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("%s error (%d): %s", c.errorName(), resp.StatusCode, string(body))
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	result := &llm.ChatResponse{}
	var content strings.Builder
	toolByIndex := map[int]int{}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		var chunk openAIStreamResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, fmt.Errorf("failed to parse stream chunk: %w", err)
		}

		if chunk.Model != "" {
			result.Model = chunk.Model
		}
		if chunk.Provenance != nil {
			result.Provenance = chunk.Provenance
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			err := fmt.Errorf("%s stream error: %s", c.errorName(), chunk.Error.Message)
			// Preserve the parent proxy's retry decision across the HTTP boundary.
			if chunk.Error.UnsafeForRetry {
				return nil, llm.UnsafeForRetry(err)
			}
			if chunk.Error.NonRetryable {
				return nil, llm.NonRetryable(err)
			}
			return nil, err
		}

		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			result.Usage = llm.TokenUsage{
				InputTokens:       chunk.Usage.PromptTokens,
				OutputTokens:      chunk.Usage.CompletionTokens,
				CachedInputTokens: chunk.Usage.PromptTokensDetails.CachedTokens,
				ReasoningTokens:   chunk.Usage.CompletionTokensDetails.ReasoningTokens,
			}
			if onEvent != nil {
				if err := onEvent(llm.StreamEvent{Type: llm.StreamEventUsage, Usage: result.Usage}); err != nil {
					return nil, err
				}
			}
		}

		for _, choice := range chunk.Choices {
			if choice.Error != nil && choice.Error.Message != "" {
				return nil, fmt.Errorf("%s model stream error: %s", c.errorName(), choice.Error.Message)
			}

			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if onEvent != nil {
					if err := onEvent(llm.StreamEvent{
						Type:         llm.StreamEventContentDelta,
						ContentDelta: choice.Delta.Content,
						Provenance:   result.Provenance,
						Model:        result.Model,
					}); err != nil {
						return nil, err
					}
				}
			}

			if choice.Delta.Refusal != "" {
				content.WriteString(choice.Delta.Refusal)
				if onEvent != nil {
					if err := onEvent(llm.StreamEvent{
						Type:         llm.StreamEventContentDelta,
						ContentDelta: choice.Delta.Refusal,
						Provenance:   result.Provenance,
						Model:        result.Model,
					}); err != nil {
						return nil, err
					}
				}
			}

			for _, tc := range choice.Delta.ToolCalls {
				idx, ok := toolByIndex[tc.Index]
				if !ok {
					result.ToolCalls = append(result.ToolCalls, llm.ToolCall{})
					idx = len(result.ToolCalls) - 1
					toolByIndex[tc.Index] = idx
				}
				if tc.ID != "" {
					result.ToolCalls[idx].ID = tc.ID
				}
				if tc.Function.Name != "" {
					result.ToolCalls[idx].Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					result.ToolCalls[idx].Input += tc.Function.Arguments
				}
				// Preserve Gemini thought signatures through OpenAI-compatible proxies so
				// replayed function calls remain valid after tool execution.
				sigDelta := tc.Function.ThoughtSignature
				if tc.ExtraContent != nil && tc.ExtraContent.Google.ThoughtSignature != "" {
					sigDelta += tc.ExtraContent.Google.ThoughtSignature
				}
				if sigDelta != "" {
					result.ToolCalls[idx].ThoughtSignature += sigDelta
				}
				if onEvent != nil {
					if err := onEvent(llm.StreamEvent{
						Type:                     llm.StreamEventToolCallDelta,
						Provenance:               result.Provenance,
						Model:                    result.Model,
						ToolCallIndex:            tc.Index,
						ToolCallID:               tc.ID,
						ToolCallName:             tc.Function.Name,
						ToolInputDelta:           tc.Function.Arguments,
						ToolCallThoughtSignature: sigDelta,
					}); err != nil {
						return nil, err
					}
				}
			}

			if choice.FinishReason != "" {
				result.StopReason = choice.FinishReason
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read error: %w", err)
	}

	result.Content = content.String()

	// If response is completely empty but finish reason indicates length or error
	if result.Content == "" && len(result.ToolCalls) == 0 && result.StopReason != "" && result.StopReason != "stop" {
		return nil, fmt.Errorf("%s model failed (finish_reason: %s)", c.errorName(), result.StopReason)
	}

	toolNames := make([]string, len(result.ToolCalls))
	for i, tc := range result.ToolCalls {
		toolNames[i] = tc.Name
	}
	logging.LogResponseWithContent(result.Usage.InputTokens, result.Usage.OutputTokens, len(result.ToolCalls), result.Content, toolNames)

	return result, nil
}
