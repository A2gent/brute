// Package lmstudio provides an LLM client for LM Studio local server
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
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const (
	defaultBaseURL   = "http://localhost:1234/v1"
	defaultMaxTokens = 4096
)

// Client implements the LLM client for LM Studio (OpenAI-compatible API)
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	isGemini   bool // Flag to enable Gemini-specific handling
}

// providerName returns the display name for the provider (used in error messages)
func (c *Client) providerName() string {
	if c.isGemini {
		return "Gemini"
	}
	base := strings.ToLower(strings.TrimSpace(c.baseURL))
	switch {
	case strings.Contains(base, "api.openai.com"):
		return "OpenAI"
	case strings.Contains(base, "openrouter.ai"):
		return "OpenRouter"
	case strings.Contains(base, "kimi.com"), strings.Contains(base, "moonshot"):
		return "Kimi"
	}
	return "LM Studio"
}

// NewClient creates a new LM Studio client
func NewClient(apiKey, model, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	// Detect if this is Gemini based on the base URL
	isGemini := strings.Contains(baseURL, "generativelanguage.googleapis.com")
	return &Client{
		apiKey:   apiKey,
		baseURL:  baseURL,
		model:    model,
		isGemini: isGemini,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute, // Local models can be slower
		},
	}
}

// openAIRequest is the request format for OpenAI-compatible API
type openAIRequest struct {
	Model               string          `json:"model"`
	Messages            []openAIMessage `json:"messages"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	Tools               []openAITool    `json:"tools,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	PromptCacheKey      string          `json:"prompt_cache_key,omitempty"`
}

// modelRequiresMaxCompletionTokens reports whether an OpenAI model rejects the
// legacy max_tokens parameter and requires max_completion_tokens instead. This
// applies to the GPT-5 family and the o-series reasoning models. Router-prefixed
// ids (e.g. "openai/gpt-5.5") are handled by stripping the vendor segment.
func modelRequiresMaxCompletionTokens(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	return strings.HasPrefix(m, "gpt-5") ||
		strings.HasPrefix(m, "o1") ||
		strings.HasPrefix(m, "o3") ||
		strings.HasPrefix(m, "o4")
}

// applyTokenLimit sets the correct token-limit field for the target model and,
// for models that only accept the default sampling temperature, omits an
// explicit temperature to avoid an "unsupported parameter" rejection.
func applyTokenLimit(req *openAIRequest, model string, maxTokens int, temperature float64) {
	if modelRequiresMaxCompletionTokens(model) {
		req.MaxCompletionTokens = maxTokens
		return
	}
	req.MaxTokens = maxTokens
	if temperature != 0 {
		req.Temperature = &temperature
	}
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	Refusal    string           `json:"refusal,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIExtraContent struct {
	Google struct {
		ThoughtSignature string `json:"thought_signature,omitempty"`
	} `json:"google,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name             string `json:"name"`
		Arguments        string `json:"arguments"`
		ThoughtSignature string `json:"thought_signature,omitempty"` // Required by Gemini
	} `json:"function"`
	ExtraContent *openAIExtraContent `json:"extra_content,omitempty"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		Parameters  map[string]interface{} `json:"parameters"`
	} `json:"function"`
}

type openAIResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
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
	} `json:"usage"`
}

type openAIStreamResponse struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content   string `json:"content"`
			Refusal   string `json:"refusal,omitempty"`
			ToolCalls []struct {
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
	} `json:"usage"`
}

// ModelsResponse represents the response from /v1/models
type ModelsResponse struct {
	Data []ModelInfo `json:"data"`
}

// ModelInfo represents a single model from LM Studio
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// ListModels fetches available models from the LM Studio server
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", c.providerName(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s returned error (%d): %s", c.providerName(), resp.StatusCode, string(body))
	}

	var modelsResp ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, fmt.Errorf("failed to parse models response: %w", err)
	}

	return modelsResp.Data, nil
}

// Chat sends a chat request to LM Studio
func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	model := request.Model
	if model == "" {
		model = c.model
	}

	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}

	// Log request
	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	// Convert messages
	messages := make([]openAIMessage, 0, len(request.Messages)+1)

	// Add system message if present
	if request.SystemPrompt != "" {
		messages = append(messages, openAIMessage{
			Role:    "system",
			Content: request.SystemPrompt,
		})
	}

	for _, msg := range request.Messages {
		oaiMsg := c.convertMessage(msg)
		messages = append(messages, oaiMsg...)
	}

	// Convert tools
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

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("%s error (%d): %s", c.providerName(), resp.StatusCode, string(body))
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	var oaiResp openAIResponse
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if oaiResp.Error != nil && oaiResp.Error.Message != "" {
		err := fmt.Errorf("%s returned error: %s", c.providerName(), oaiResp.Error.Message)
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no response from %s", c.providerName())
	}

	choice := oaiResp.Choices[0]
	if choice.Error != nil && choice.Error.Message != "" {
		err := fmt.Errorf("%s model error: %s", c.providerName(), choice.Error.Message)
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	responseText, responseImages := parseOpenAIContent(choice.Message.Content)
	if responseText == "" && choice.Message.Refusal != "" {
		responseText = choice.Message.Refusal
	}

	// If response is completely empty but finish reason indicates length or error
	if responseText == "" && len(choice.Message.ToolCalls) == 0 && choice.FinishReason != "" && choice.FinishReason != "stop" {
		return nil, fmt.Errorf("%s model failed (finish_reason: %s)", c.providerName(), choice.FinishReason)
	}

	response := &llm.ChatResponse{
		Content:    responseText,
		Images:     responseImages,
		StopReason: choice.FinishReason,
		Usage: llm.TokenUsage{
			InputTokens:       oaiResp.Usage.PromptTokens,
			OutputTokens:      oaiResp.Usage.CompletionTokens,
			CachedInputTokens: oaiResp.Usage.PromptTokensDetails.CachedTokens,
		},
	}

	// Convert tool calls
	for _, tc := range choice.Message.ToolCalls {
		thoughtSig := tc.Function.ThoughtSignature
		if tc.ExtraContent != nil && tc.ExtraContent.Google.ThoughtSignature != "" {
			thoughtSig = tc.ExtraContent.Google.ThoughtSignature
		}
		response.ToolCalls = append(response.ToolCalls, llm.ToolCall{
			ID:               tc.ID,
			Name:             tc.Function.Name,
			Input:            tc.Function.Arguments,
			ThoughtSignature: thoughtSig,
		})
	}

	// Log response
	toolNames := make([]string, len(response.ToolCalls))
	for i, tc := range response.ToolCalls {
		toolNames[i] = tc.Name
	}
	logging.LogResponseWithContent(response.Usage.InputTokens, response.Usage.OutputTokens, len(response.ToolCalls), response.Content, toolNames)

	return response, nil
}

// ChatStream sends a streaming chat request to LM Studio.
func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	model := request.Model
	if model == "" {
		model = c.model
	}

	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}

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

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("%s error (%d): %s", c.providerName(), resp.StatusCode, string(body))
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	result := &llm.ChatResponse{}
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

		if chunk.Error != nil && chunk.Error.Message != "" {
			return nil, fmt.Errorf("%s stream error: %s", c.providerName(), chunk.Error.Message)
		}

		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			result.Usage = llm.TokenUsage{
				InputTokens:       chunk.Usage.PromptTokens,
				OutputTokens:      chunk.Usage.CompletionTokens,
				CachedInputTokens: chunk.Usage.PromptTokensDetails.CachedTokens,
			}
			if onEvent != nil {
				if err := onEvent(llm.StreamEvent{Type: llm.StreamEventUsage, Usage: result.Usage}); err != nil {
					return nil, err
				}
			}
		}

		for _, choice := range chunk.Choices {
			if choice.Error != nil && choice.Error.Message != "" {
				return nil, fmt.Errorf("%s model stream error: %s", c.providerName(), choice.Error.Message)
			}

			if choice.Delta.Content != "" {
				result.Content += choice.Delta.Content
				if onEvent != nil {
					if err := onEvent(llm.StreamEvent{
						Type:         llm.StreamEventContentDelta,
						ContentDelta: choice.Delta.Content,
					}); err != nil {
						return nil, err
					}
				}
			}

			if choice.Delta.Refusal != "" {
				result.Content += choice.Delta.Refusal
				if onEvent != nil {
					if err := onEvent(llm.StreamEvent{
						Type:         llm.StreamEventContentDelta,
						ContentDelta: choice.Delta.Refusal,
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

	// If response is completely empty but finish reason indicates length or error
	if result.Content == "" && len(result.ToolCalls) == 0 && result.StopReason != "" && result.StopReason != "stop" {
		return nil, fmt.Errorf("%s model failed (finish_reason: %s)", c.providerName(), result.StopReason)
	}

	toolNames := make([]string, len(result.ToolCalls))
	for i, tc := range result.ToolCalls {
		toolNames[i] = tc.Name
	}
	logging.LogResponseWithContent(result.Usage.InputTokens, result.Usage.OutputTokens, len(result.ToolCalls), result.Content, toolNames)

	return result, nil
}

// convertMessage converts an LLM message to OpenAI format
func (c *Client) convertMessage(msg llm.Message) []openAIMessage {
	if msg.Role == "tool" {
		// Tool results in OpenAI format
		var messages []openAIMessage
		for _, result := range msg.ToolResults {
			messages = append(messages, openAIMessage{
				Role:       "tool",
				Content:    result.Content,
				ToolCallID: result.ToolCallID,
			})
		}
		return messages
	}

	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		// Assistant with tool calls

		// Find the first thought_signature in the block to share across all tool calls
		sharedSignature := "skip_thought_signature_validator"
		for _, tc := range msg.ToolCalls {
			if tc.ThoughtSignature != "" {
				sharedSignature = tc.ThoughtSignature
				break
			}
		}

		var toolCalls []openAIToolCall
		for _, tc := range msg.ToolCalls {
			toolCall := openAIToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: struct {
					Name             string `json:"name"`
					Arguments        string `json:"arguments"`
					ThoughtSignature string `json:"thought_signature,omitempty"`
				}{
					Name:      tc.Name,
					Arguments: tc.Input,
				},
			}

			// Add thought signature for Gemini (on all tool calls, both in extra_content and function)
			if c.isGemini {
				sig := sharedSignature
				if tc.ThoughtSignature != "" {
					sig = tc.ThoughtSignature
				}

				toolCall.ExtraContent = &openAIExtraContent{
					Google: struct {
						ThoughtSignature string `json:"thought_signature,omitempty"`
					}{
						ThoughtSignature: sig,
					},
				}
				toolCall.Function.ThoughtSignature = sig
			}
			toolCalls = append(toolCalls, toolCall)
		}
		return []openAIMessage{{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: toolCalls,
		}}
	}

	// Simple text message
	if msg.Role == "user" && len(msg.Images) > 0 {
		return []openAIMessage{{
			Role:    msg.Role,
			Content: buildOpenAIUserContent(msg.Content, msg.Images),
		}}
	}
	return []openAIMessage{{
		Role:    msg.Role,
		Content: msg.Content,
	}}
}

func buildOpenAIUserContent(text string, images []llm.Image) []map[string]interface{} {
	parts := make([]map[string]interface{}, 0, len(images)+1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, map[string]interface{}{
			"type": "text",
			"text": text,
		})
	}
	for _, img := range images {
		url := strings.TrimSpace(img.URL)
		if url == "" {
			url = img.DataURL()
		}
		if url == "" {
			continue
		}
		parts = append(parts, map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]interface{}{
				"url": url,
			},
		})
	}
	if len(parts) == 0 && strings.TrimSpace(text) != "" {
		parts = append(parts, map[string]interface{}{
			"type": "text",
			"text": text,
		})
	}
	return parts
}

func parseOpenAIContent(content any) (string, []llm.Image) {
	switch v := content.(type) {
	case string:
		return v, nil
	case []interface{}:
		return parseOpenAIContentParts(v)
	default:
		return "", nil
	}
}

func parseOpenAIContentParts(parts []interface{}) (string, []llm.Image) {
	var builder strings.Builder
	images := make([]llm.Image, 0)
	for _, raw := range parts {
		part, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		partType := strings.TrimSpace(asString(part["type"]))
		switch partType {
		case "text", "output_text", "input_text":
			text := asString(part["text"])
			if text != "" {
				if builder.Len() > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(text)
			}
		case "image_url", "output_image":
			imageURL := ""
			if imageObj, ok := part["image_url"].(map[string]interface{}); ok {
				imageURL = strings.TrimSpace(asString(imageObj["url"]))
			}
			if imageURL == "" {
				imageURL = strings.TrimSpace(asString(part["url"]))
			}
			if imageURL != "" {
				images = append(images, llm.Image{URL: imageURL})
			}
		}
	}
	return builder.String(), images
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

// Ensure Client implements llm.Client
var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
