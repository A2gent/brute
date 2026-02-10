package kimi

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
	defaultBaseURL = "https://api.moonshot.cn/v1"
	defaultModel   = "kimi-k2.5"
)

// Client implements the LLM client for Kimi K2.5 (Moonshot AI)
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewClient creates a new Kimi client
func NewClient(apiKey, model string) *Client {
	if model == "" {
		model = defaultModel
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// kimiRequest is the request format for Kimi API (OpenAI-compatible)
type kimiRequest struct {
	Model       string        `json:"model"`
	Messages    []kimiMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Tools       []kimiTool    `json:"tools,omitempty"`
	ToolChoice  string        `json:"tool_choice,omitempty"`
}

type kimiMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []kimiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type kimiTool struct {
	Type     string       `json:"type"`
	Function kimiFunction `json:"function"`
}

type kimiFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type kimiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type kimiResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int         `json:"index"`
		Message kimiMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type kimiError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Chat sends a chat request to Kimi API
func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	model := request.Model
	if model == "" {
		model = c.model
	}

	// Log request with last message content
	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	// Convert messages
	messages := make([]kimiMessage, 0, len(request.Messages)+1)

	// Add system prompt if present
	if request.SystemPrompt != "" {
		messages = append(messages, kimiMessage{
			Role:    "system",
			Content: request.SystemPrompt,
		})
	}

	for _, msg := range request.Messages {
		kimiMsg := c.convertMessage(msg)
		messages = append(messages, kimiMsg)
	}

	// Convert tools
	tools := make([]kimiTool, 0, len(request.Tools))
	for _, t := range request.Tools {
		tools = append(tools, kimiTool{
			Type: "function",
			Function: kimiFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	reqBody := kimiRequest{
		Model:       model,
		Messages:    messages,
		Temperature: request.Temperature,
		MaxTokens:   request.MaxTokens,
		Tools:       tools,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Make HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

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
		var errResp kimiError
		json.Unmarshal(body, &errResp)
		err := fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error.Message)
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	var kimiResp kimiResponse
	if err := json.Unmarshal(body, &kimiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(kimiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	choice := kimiResp.Choices[0].Message

	// Convert response
	response := &llm.ChatResponse{
		Content: choice.Content,
		Usage: llm.TokenUsage{
			InputTokens:  kimiResp.Usage.PromptTokens,
			OutputTokens: kimiResp.Usage.CompletionTokens,
		},
	}

	// Convert tool calls
	for _, tc := range choice.ToolCalls {
		response.ToolCalls = append(response.ToolCalls, llm.ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: tc.Function.Arguments,
		})
	}

	// Log response with content and tool names
	toolNames := make([]string, len(response.ToolCalls))
	for i, tc := range response.ToolCalls {
		toolNames[i] = tc.Name
	}
	logging.LogResponseWithContent(response.Usage.InputTokens, response.Usage.OutputTokens, len(response.ToolCalls), response.Content, toolNames)

	return response, nil
}

// convertMessage converts an LLM message to Kimi format
func (c *Client) convertMessage(msg llm.Message) kimiMessage {
	if msg.Role == "tool" {
		// Tool results - Kimi uses "tool" role for results
		if len(msg.ToolResults) > 0 {
			return kimiMessage{
				Role:       "tool",
				Content:    msg.ToolResults[0].Content,
				ToolCallID: msg.ToolResults[0].ToolCallID,
			}
		}
		return kimiMessage{
			Role:    "tool",
			Content: msg.Content,
		}
	}

	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		// Assistant with tool calls
		toolCalls := make([]kimiToolCall, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			toolCalls = append(toolCalls, kimiToolCall{
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
		return kimiMessage{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: toolCalls,
		}
	}

	// Simple text message
	return kimiMessage{
		Role:    msg.Role,
		Content: msg.Content,
	}
}

// Ensure Client implements llm.Client
var _ llm.Client = (*Client)(nil)
