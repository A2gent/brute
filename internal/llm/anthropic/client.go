package anthropic

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
	defaultBaseURL    = "https://api.anthropic.com/v1"
	defaultAPIVersion = "2023-06-01"
	defaultMaxTokens  = 8192
)

// Client implements the LLM client for Anthropic Claude
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewClient creates a new Anthropic client
func NewClient(apiKey, model string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// NewClientWithBaseURL creates a new Anthropic-compatible client with a custom base URL
func NewClientWithBaseURL(apiKey, model, baseURL string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// anthropicRequest is the request format for Anthropic API
type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []contentBlock
}

type contentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Content      []contentBlock `json:"content"`
	Model        string         `json:"model"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Chat sends a chat request to Anthropic
func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	// Build Anthropic request
	model := request.Model
	if model == "" {
		model = c.model
	}

	maxTokens := request.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}

	// Log request with last message content
	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	// Convert messages
	messages := make([]anthropicMessage, 0, len(request.Messages))
	for _, msg := range request.Messages {
		anthroMsg := c.convertMessage(msg)
		messages = append(messages, anthroMsg)
	}

	// Convert tools
	tools := make([]anthropicTool, 0, len(request.Tools))
	for _, t := range request.Tools {
		tools = append(tools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	reqBody := anthropicRequest{
		Model:       model,
		Messages:    messages,
		System:      request.SystemPrompt,
		MaxTokens:   maxTokens,
		Temperature: request.Temperature,
		Tools:       tools,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Make HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", defaultAPIVersion)

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
		var errResp struct {
			Error anthropicError `json:"error"`
		}
		json.Unmarshal(body, &errResp)
		err := fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error.Message)
		logging.LogResponse(0, 0, 0, err)
		logging.Debug("Response body: %s", string(body))
		return nil, err
	}

	var anthroResp anthropicResponse
	if err := json.Unmarshal(body, &anthroResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Convert response
	response := &llm.ChatResponse{
		StopReason: anthroResp.StopReason,
		Usage: llm.TokenUsage{
			InputTokens:  anthroResp.Usage.InputTokens,
			OutputTokens: anthroResp.Usage.OutputTokens,
		},
	}

	// Extract content and tool calls
	var textParts []string
	for _, block := range anthroResp.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			inputJSON, _ := json.Marshal(block.Input)
			response.ToolCalls = append(response.ToolCalls, llm.ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: string(inputJSON),
			})
		}
	}

	if len(textParts) > 0 {
		response.Content = textParts[0]
		for i := 1; i < len(textParts); i++ {
			response.Content += "\n" + textParts[i]
		}
	}

	// Log response with content and tool names
	toolNames := make([]string, len(response.ToolCalls))
	for i, tc := range response.ToolCalls {
		toolNames[i] = tc.Name
	}
	logging.LogResponseWithContent(response.Usage.InputTokens, response.Usage.OutputTokens, len(response.ToolCalls), response.Content, toolNames)

	return response, nil
}

// convertMessage converts an LLM message to Anthropic format
func (c *Client) convertMessage(msg llm.Message) anthropicMessage {
	if msg.Role == "tool" {
		// Tool results need special handling
		blocks := make([]contentBlock, 0, len(msg.ToolResults))
		for _, result := range msg.ToolResults {
			blocks = append(blocks, contentBlock{
				Type:      "tool_result",
				ToolUseID: result.ToolCallID,
				Content:   result.Content,
				IsError:   result.IsError,
			})
		}
		return anthropicMessage{
			Role:    "user",
			Content: blocks,
		}
	}

	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		// Assistant with tool calls
		blocks := make([]contentBlock, 0)
		if msg.Content != "" {
			blocks = append(blocks, contentBlock{
				Type: "text",
				Text: msg.Content,
			})
		}
		for _, tc := range msg.ToolCalls {
			var input any
			json.Unmarshal([]byte(tc.Input), &input)
			blocks = append(blocks, contentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Name,
				Input: input,
			})
		}
		return anthropicMessage{
			Role:    "assistant",
			Content: blocks,
		}
	}

	// Simple text message
	return anthropicMessage{
		Role:    msg.Role,
		Content: msg.Content,
	}
}

// Ensure Client implements llm.Client
var _ llm.Client = (*Client)(nil)
