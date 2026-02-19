package kimi

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
	Stream      bool          `json:"stream,omitempty"`
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

type kimiStreamResponse struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
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
		messages = append(messages, c.convertMessage(msg)...)
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

// ChatStream sends a streaming chat request to Kimi API.
func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	model := request.Model
	if model == "" {
		model = c.model
	}

	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	messages := make([]kimiMessage, 0, len(request.Messages)+1)
	if request.SystemPrompt != "" {
		messages = append(messages, kimiMessage{
			Role:    "system",
			Content: request.SystemPrompt,
		})
	}
	for _, msg := range request.Messages {
		messages = append(messages, c.convertMessage(msg)...)
	}

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
		Stream:      true,
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
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var errResp kimiError
		_ = json.Unmarshal(body, &errResp)
		err := fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error.Message)
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

		var chunk kimiStreamResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, fmt.Errorf("failed to parse stream chunk: %w", err)
		}

		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			result.Usage = llm.TokenUsage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
			if onEvent != nil {
				if err := onEvent(llm.StreamEvent{Type: llm.StreamEventUsage, Usage: result.Usage}); err != nil {
					return nil, err
				}
			}
		}

		for _, choice := range chunk.Choices {
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
				if onEvent != nil {
					if err := onEvent(llm.StreamEvent{
						Type:           llm.StreamEventToolCallDelta,
						ToolCallIndex:  tc.Index,
						ToolCallID:     tc.ID,
						ToolCallName:   tc.Function.Name,
						ToolInputDelta: tc.Function.Arguments,
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

	toolNames := make([]string, len(result.ToolCalls))
	for i, tc := range result.ToolCalls {
		toolNames[i] = tc.Name
	}
	logging.LogResponseWithContent(result.Usage.InputTokens, result.Usage.OutputTokens, len(result.ToolCalls), result.Content, toolNames)
	return result, nil
}

// convertMessage converts an LLM message to Kimi format.
// For tool role messages, Kimi expects one message per tool result.
func (c *Client) convertMessage(msg llm.Message) []kimiMessage {
	if msg.Role == "tool" {
		// Tool results - Kimi uses "tool" role for results
		if len(msg.ToolResults) > 0 {
			out := make([]kimiMessage, 0, len(msg.ToolResults))
			for _, result := range msg.ToolResults {
				out = append(out, kimiMessage{
					Role:       "tool",
					Content:    result.Content,
					ToolCallID: result.ToolCallID,
				})
			}
			return out
		}
		return []kimiMessage{{
			Role:    "tool",
			Content: msg.Content,
		}}
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
		return []kimiMessage{{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: toolCalls,
		}}
	}

	// Simple text message
	return []kimiMessage{{
		Role:    msg.Role,
		Content: msg.Content,
	}}
}

// Ensure Client implements llm.Client
var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
