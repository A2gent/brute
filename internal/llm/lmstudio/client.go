// Package lmstudio provides an LLM client for LM Studio local server
package lmstudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gratheon/aagent/internal/llm"
	"github.com/gratheon/aagent/internal/logging"
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
}

// NewClient creates a new LM Studio client
func NewClient(apiKey, model, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute, // Local models can be slower
		},
	}
}

// openAIRequest is the request format for OpenAI-compatible API
type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	Tools       []openAITool    `json:"tools,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
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
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
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
		return nil, fmt.Errorf("failed to connect to LM Studio: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("LM Studio returned error (%d): %s", resp.StatusCode, string(body))
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
		Model:       model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: request.Temperature,
		Tools:       tools,
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
		err := fmt.Errorf("LM Studio error (%d): %s", resp.StatusCode, string(body))
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	var oaiResp openAIResponse
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no response from LM Studio")
	}

	choice := oaiResp.Choices[0]
	response := &llm.ChatResponse{
		Content:    choice.Message.Content,
		StopReason: choice.FinishReason,
		Usage: llm.TokenUsage{
			InputTokens:  oaiResp.Usage.PromptTokens,
			OutputTokens: oaiResp.Usage.CompletionTokens,
		},
	}

	// Convert tool calls
	for _, tc := range choice.Message.ToolCalls {
		response.ToolCalls = append(response.ToolCalls, llm.ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: tc.Function.Arguments,
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
		var toolCalls []openAIToolCall
		for _, tc := range msg.ToolCalls {
			toolCalls = append(toolCalls, openAIToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{
					Name:      tc.Name,
					Arguments: tc.Input,
				},
			})
		}
		return []openAIMessage{{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: toolCalls,
		}}
	}

	// Simple text message
	return []openAIMessage{{
		Role:    msg.Role,
		Content: msg.Content,
	}}
}

// Ensure Client implements llm.Client
var _ llm.Client = (*Client)(nil)
