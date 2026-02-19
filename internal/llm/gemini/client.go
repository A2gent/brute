// Package gemini provides an LLM client for Google Gemini API
package gemini

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
	defaultBaseURL   = "https://generativelanguage.googleapis.com/v1beta/openai"
	defaultMaxTokens = 4096
)

// Client implements the LLM client for Google Gemini (OpenAI-compatible API with Gemini extensions)
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewClient creates a new Gemini client
func NewClient(apiKey, model, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}
}

// geminiRequest is the request format for Gemini's OpenAI-compatible API
type geminiRequest struct {
	Model       string          `json:"model"`
	Messages    []geminiMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	Tools       []geminiTool    `json:"tools,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

type geminiMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []geminiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"` // Function name for tool results
}

type geminiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name             string `json:"name"`
		Arguments        string `json:"arguments"`
		ThoughtSignature string `json:"thought_signature"` // Required by Gemini
	} `json:"function"`
}

type geminiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		Parameters  map[string]interface{} `json:"parameters"`
	} `json:"function"`
}

type geminiResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int           `json:"index"`
		Message      geminiMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type geminiStreamResponse struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name             string `json:"name"`
					Arguments        string `json:"arguments"`
					ThoughtSignature string `json:"thought_signature,omitempty"`
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

// ModelInfo represents a single model from Gemini
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelsResponse represents the response from /v1/models
type ModelsResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

// ListModels fetches available models from the Gemini server
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Gemini: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Gemini returned error (%d): %s", resp.StatusCode, string(body))
	}

	var modelsResp ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, fmt.Errorf("failed to parse models response: %w", err)
	}

	return modelsResp.Data, nil
}

// Chat sends a chat request to Gemini
func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	model := c.model
	if request.Model != "" {
		model = request.Model
	}

	maxTokens := defaultMaxTokens
	if request.MaxTokens > 0 {
		maxTokens = request.MaxTokens
	}

	// Build messages
	var messages []geminiMessage
	if request.SystemPrompt != "" {
		messages = append(messages, geminiMessage{
			Role:    "system",
			Content: request.SystemPrompt,
		})
	}

	for _, msg := range request.Messages {
		geminiMsg := c.convertMessage(msg)
		messages = append(messages, geminiMsg...)
	}

	// Convert tools
	var tools []geminiTool
	for _, t := range request.Tools {
		tools = append(tools, geminiTool{
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

	reqBody := geminiRequest{
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

	// Debug: log full request
	logging.Debug("Gemini Chat request: %s", string(jsonBody))

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
		err := fmt.Errorf("Gemini error (%d): %s", resp.StatusCode, string(body))
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(geminiResp.Choices) == 0 {
		return nil, fmt.Errorf("no response from Gemini")
	}

	choice := geminiResp.Choices[0]
	response := &llm.ChatResponse{
		Content:    choice.Message.Content,
		StopReason: choice.FinishReason,
		Usage: llm.TokenUsage{
			InputTokens:  geminiResp.Usage.PromptTokens,
			OutputTokens: geminiResp.Usage.CompletionTokens,
		},
	}

	// Convert tool calls
	for _, tc := range choice.Message.ToolCalls {
		response.ToolCalls = append(response.ToolCalls, llm.ToolCall{
			ID:               tc.ID,
			Name:             tc.Function.Name,
			Input:            tc.Function.Arguments,
			ThoughtSignature: tc.Function.ThoughtSignature,
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

// ChatStream sends a streaming chat request to Gemini.
func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	model := c.model
	if request.Model != "" {
		model = request.Model
	}

	maxTokens := defaultMaxTokens
	if request.MaxTokens > 0 {
		maxTokens = request.MaxTokens
	}

	// Build messages
	var messages []geminiMessage
	if request.SystemPrompt != "" {
		messages = append(messages, geminiMessage{
			Role:    "system",
			Content: request.SystemPrompt,
		})
	}

	for _, msg := range request.Messages {
		geminiMsg := c.convertMessage(msg)
		messages = append(messages, geminiMsg...)
	}

	// Convert tools
	var tools []geminiTool
	for _, t := range request.Tools {
		tools = append(tools, geminiTool{
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

	reqBody := geminiRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: request.Temperature,
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
		err := fmt.Errorf("Gemini error (%d): %s", resp.StatusCode, string(body))
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

		var chunk geminiStreamResponse
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
				if tc.Function.ThoughtSignature != "" {
					result.ToolCalls[idx].ThoughtSignature = tc.Function.ThoughtSignature
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

// convertMessage converts an LLM message to Gemini format
func (c *Client) convertMessage(msg llm.Message) []geminiMessage {
	if msg.Role == "tool" {
		// Tool results in Gemini format
		var messages []geminiMessage
		for _, result := range msg.ToolResults {
			messages = append(messages, geminiMessage{
				Role:       "tool",
				Content:    result.Content,
				ToolCallID: result.ToolCallID,
				Name:       result.Name, // Required by Gemini
			})
		}
		return messages
	}

	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		// Assistant with tool calls - preserve or generate thought_signature for Gemini
		var toolCalls []geminiToolCall
		for _, tc := range msg.ToolCalls {
			var toolCall geminiToolCall
			toolCall.ID = tc.ID
			toolCall.Type = "function"
			toolCall.Function.Name = tc.Name
			toolCall.Function.Arguments = tc.Input
			// Use saved thought_signature or generate default
			if tc.ThoughtSignature != "" {
				toolCall.Function.ThoughtSignature = tc.ThoughtSignature
				logging.Debug("Using saved thought_signature for %s: %s", tc.Name, tc.ThoughtSignature)
			} else {
				toolCall.Function.ThoughtSignature = "Calling tool: " + tc.Name
				logging.Debug("Generated thought_signature for %s: %s", tc.Name, toolCall.Function.ThoughtSignature)
			}
			toolCalls = append(toolCalls, toolCall)
		}
		return []geminiMessage{{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: toolCalls,
		}}
	}

	// Simple text message
	return []geminiMessage{{
		Role:    msg.Role,
		Content: msg.Content,
	}}
}

// Ensure Client implements llm.Client
var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
