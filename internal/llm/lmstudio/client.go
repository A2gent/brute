// Package lmstudio provides an LLM client for LM Studio local server
package lmstudio

import (
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
	// PrepareRequest optionally supplies per-invocation headers and cleanup.
	PrepareRequest func(context.Context, *llm.ChatRequest) (http.Header, func(), error)
	// ErrorLabel names the real upstream in errors when this client only proxies
	// it (e.g. a child agent's parent proxy), instead of the URL-derived "LM Studio".
	ErrorLabel string

	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	isGemini   bool // Flag to enable Gemini-specific handling
}

// providerName returns the display name for the provider (used in error messages)
// effectiveMaxTokens returns the output token cap to send. For OpenRouter an
// unset cap stays 0 (omitted from the request): reasoning models count hidden
// reasoning tokens against it, and the 4096 default made them fail with
// finish_reason=length before producing any visible output.
// Parent-proxy clients (ErrorLabel set) also keep 0: their localhost URL looks
// like LM Studio, but the real upstream (often OpenRouter) is chosen by the
// parent, which applies its own default when the cap is omitted.
func (c *Client) effectiveMaxTokens(requested int) int {
	if requested == 0 && c.providerName() != "OpenRouter" && strings.TrimSpace(c.ErrorLabel) == "" {
		return defaultMaxTokens
	}
	return requested
}

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
	case strings.Contains(base, "api.meta.ai"):
		return "Meta"
	}
	return "LM Studio"
}

func (c *Client) errorName() string {
	if label := strings.TrimSpace(c.ErrorLabel); label != "" {
		return label
	}
	return c.providerName()
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
			Transport: llm.SharedHTTPTransport(0),
			Timeout:   10 * time.Minute, // Local models can be slower
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
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	PromptCacheKey string         `json:"prompt_cache_key,omitempty"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
}

// modelRequiresMaxCompletionTokens reports whether an OpenAI model rejects the
// legacy max_tokens parameter and requires max_completion_tokens instead. This
// applies to GPT-5+, GPT-6, and the o-series reasoning models. Router-prefixed
// ids (e.g. "openai/gpt-5.5") are handled by stripping the vendor segment.
func modelRequiresMaxCompletionTokens(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	return strings.HasPrefix(m, "gpt-5") ||
		strings.HasPrefix(m, "gpt-6") ||
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
	// ReasoningContent is response-only; omitempty keeps it out of requests.
	ReasoningContent string `json:"reasoning_content,omitempty"`
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
		return nil, fmt.Errorf("failed to connect to %s: %w", c.errorName(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s returned error (%d): %s", c.errorName(), resp.StatusCode, string(body))
	}

	var modelsResp ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, fmt.Errorf("failed to parse models response: %w", err)
	}

	return modelsResp.Data, nil
}

// Chat sends a chat request to LM Studio
func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
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
	if request.JSONSchema != nil {
		reqBody.ResponseFormat = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "strict": true, "schema": request.JSONSchema}}
	}

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
		err := fmt.Errorf("%s error (%d): %s", c.errorName(), resp.StatusCode, string(body))
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	var oaiResp openAIResponse
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if oaiResp.Error != nil && oaiResp.Error.Message != "" {
		err := fmt.Errorf("%s returned error: %s", c.errorName(), oaiResp.Error.Message)
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no response from %s", c.errorName())
	}

	choice := oaiResp.Choices[0]
	if choice.Error != nil && choice.Error.Message != "" {
		err := fmt.Errorf("%s model error: %s", c.errorName(), choice.Error.Message)
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	responseText, responseImages := parseOpenAIContent(choice.Message.Content)
	if responseText == "" && choice.Message.Refusal != "" {
		responseText = choice.Message.Refusal
	}
	// LM Studio routes schema-constrained output of reasoning models into reasoning_content.
	if responseText == "" && request.JSONSchema != nil {
		responseText = strings.TrimSpace(choice.Message.ReasoningContent)
	}

	// If response is completely empty but finish reason indicates length or error
	if responseText == "" && len(choice.Message.ToolCalls) == 0 && choice.FinishReason != "" && choice.FinishReason != "stop" {
		return nil, fmt.Errorf("%s model failed (finish_reason: %s)", c.errorName(), choice.FinishReason)
	}

	response := &llm.ChatResponse{
		Provenance: oaiResp.Provenance,
		Model:      oaiResp.Model,
		Content:    responseText,
		Images:     responseImages,
		StopReason: choice.FinishReason,
		Usage: llm.TokenUsage{
			InputTokens:       oaiResp.Usage.PromptTokens,
			OutputTokens:      oaiResp.Usage.CompletionTokens,
			CachedInputTokens: oaiResp.Usage.PromptTokensDetails.CachedTokens,
			ReasoningTokens:   oaiResp.Usage.CompletionTokensDetails.ReasoningTokens,
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
