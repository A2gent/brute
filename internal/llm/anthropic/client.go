package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const (
	defaultBaseURL    = "https://api.anthropic.com/v1"
	defaultAPIVersion = "2023-06-01"
	defaultMaxTokens  = 8192
	// Beta features required for Claude Code compatibility
	claudeCodeBetaHeader = "claude-code-20250219,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14"
	// OAuth beta feature
	oauthBetaFeature = "oauth-2025-04-20"
)

// Client implements the LLM client for Anthropic Claude
type Client struct {
	apiKey       string
	baseURL      string
	model        string
	httpClient   *http.Client
	isClaudeCode bool // Enable Claude Code branding and features

	// OAuth support
	oauth          *OAuthTokens
	refreshHandler func(string) (*OAuthTokens, error) // Callback to refresh tokens
}

// NewClient creates a new Anthropic client
func NewClient(apiKey, model string) *Client {
	return &Client{
		apiKey:       apiKey,
		baseURL:      defaultBaseURL,
		model:        model,
		isClaudeCode: true, // Default to Claude Code mode for better compatibility
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// NewClientWithBaseURL creates a new Anthropic-compatible client with a custom base URL
func NewClientWithBaseURL(apiKey, model, baseURL string) *Client {
	return &Client{
		apiKey:       apiKey,
		baseURL:      baseURL,
		model:        model,
		isClaudeCode: true,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// WithClaudeCodeMode enables/disables Claude Code branding and beta features
func (c *Client) WithClaudeCodeMode(enabled bool) *Client {
	c.isClaudeCode = enabled
	return c
}

// NewOAuthClient creates a new Anthropic client with OAuth tokens
func NewOAuthClient(tokens *OAuthTokens, model string, refreshHandler func(string) (*OAuthTokens, error)) *Client {
	return &Client{
		baseURL:        defaultBaseURL,
		model:          model,
		isClaudeCode:   true,
		oauth:          tokens,
		refreshHandler: refreshHandler,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// WithOAuth sets OAuth tokens for the client
func (c *Client) WithOAuth(tokens *OAuthTokens, refreshHandler func(string) (*OAuthTokens, error)) *Client {
	c.oauth = tokens
	c.refreshHandler = refreshHandler
	return c
}

// isUsingOAuth returns true if client is configured for OAuth
func (c *Client) isUsingOAuth() bool {
	return c.oauth != nil && c.oauth.AccessToken != ""
}

// validateToolSequence validates and cleans up tool_use/tool_result pairs
// to prevent API errors about orphaned tool results
func (c *Client) validateToolSequence(messages []anthropicMessage) []anthropicMessage {
	// Track tool_use IDs that have been used
	usedToolIds := make(map[string]bool)
	
	// First pass: collect all tool_use IDs
	for _, msg := range messages {
		if blocks, ok := msg.Content.([]contentBlock); ok {
			for _, block := range blocks {
				if block.Type == "tool_use" && block.ID != "" {
					usedToolIds[block.ID] = true
				}
			}
		}
	}
	
	// Second pass: filter out tool_result blocks with orphaned tool_use_ids
	cleanedMessages := make([]anthropicMessage, 0, len(messages))
	removedResults := 0
	
	for _, msg := range messages {
		if blocks, ok := msg.Content.([]contentBlock); ok {
			cleanedBlocks := make([]contentBlock, 0, len(blocks))
			for _, block := range blocks {
				if block.Type == "tool_result" {
					// Only include tool_result if corresponding tool_use exists
					if block.ToolUseID != "" && usedToolIds[block.ToolUseID] {
						cleanedBlocks = append(cleanedBlocks, block)
					} else {
						removedResults++
						logging.Debug("Removed orphaned tool_result with tool_use_id: %s", block.ToolUseID)
					}
				} else {
					cleanedBlocks = append(cleanedBlocks, block)
				}
			}
			
			// Only include message if it has content blocks
			if len(cleanedBlocks) > 0 {
				msg.Content = cleanedBlocks
				cleanedMessages = append(cleanedMessages, msg)
			}
		} else {
			// Non-block content, include as-is
			cleanedMessages = append(cleanedMessages, msg)
		}
	}
	
	if removedResults > 0 {
		logging.Debug("Cleaned up %d orphaned tool results from message sequence", removedResults)
	}
	
	return cleanedMessages
}

// anthropicRequest is the request format for Anthropic API
type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
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
	Content   any    `json:"content,omitempty"`
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

type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
}

// transformSystemPrompt adds Claude Code prefix if enabled
func (c *Client) transformSystemPrompt(systemPrompt string) string {
	if !c.isClaudeCode {
		return systemPrompt
	}
	prefix := "You are Claude Code, Anthropic's official CLI for Claude."
	if systemPrompt == "" {
		return prefix
	}
	return prefix + "\n\n" + systemPrompt
}

// refreshOAuthTokenIfNeeded checks and refreshes OAuth token if expired
func (c *Client) refreshOAuthTokenIfNeeded() error {
	if !c.isUsingOAuth() {
		return nil
	}

	// Calculate expiration time (token expires_in seconds from now)
	expiresAt := time.Now().Unix() + int64(c.oauth.ExpiresIn)

	// Check if token is expired (with 5 minute buffer)
	if !IsTokenExpired(expiresAt) {
		return nil
	}

	// Refresh token
	if c.refreshHandler == nil {
		return fmt.Errorf("OAuth token expired but no refresh handler configured")
	}

	newTokens, err := c.refreshHandler(c.oauth.RefreshToken)
	if err != nil {
		return fmt.Errorf("failed to refresh OAuth token: %w", err)
	}

	c.oauth = newTokens
	return nil
}

// prepareHeaders sets up HTTP headers for API request
func (c *Client) prepareHeaders(req *http.Request) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", defaultAPIVersion)

	// OAuth or API key authentication
	if c.isUsingOAuth() {
		// Refresh token if needed
		if err := c.refreshOAuthTokenIfNeeded(); err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.oauth.AccessToken)

		// Add OAuth beta header
		if c.isClaudeCode {
			req.Header.Set("anthropic-beta", claudeCodeBetaHeader+","+oauthBetaFeature)
		} else {
			req.Header.Set("anthropic-beta", oauthBetaFeature)
		}
	} else {
		// API key authentication
		req.Header.Set("x-api-key", c.apiKey)

		// Add Claude Code beta headers if enabled
		if c.isClaudeCode {
			req.Header.Set("anthropic-beta", claudeCodeBetaHeader)
		}
	}

	return nil
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
	
	// Validate and clean up tool use/result pairs
	messages = c.validateToolSequence(messages)

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
		System:      c.transformSystemPrompt(request.SystemPrompt),
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

	// Set headers (handles both OAuth and API key auth)
	if err := c.prepareHeaders(httpReq); err != nil {
		return nil, err
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

// ChatStream sends a streaming chat request to Anthropic.
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

	messages := make([]anthropicMessage, 0, len(request.Messages))
	for _, msg := range request.Messages {
		messages = append(messages, c.convertMessage(msg))
	}
	
	// Validate and clean up tool use/result pairs
	messages = c.validateToolSequence(messages)

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
		System:      c.transformSystemPrompt(request.SystemPrompt),
		MaxTokens:   maxTokens,
		Temperature: request.Temperature,
		Tools:       tools,
		Stream:      true,
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers (handles both OAuth and API key auth)
	if err := c.prepareHeaders(httpReq); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var errResp struct {
			Error anthropicError `json:"error"`
		}
		_ = json.Unmarshal(body, &errResp)
		err := fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error.Message)
		logging.LogResponse(0, 0, 0, err)
		logging.Debug("Response body: %s", string(body))
		return nil, err
	}

	result := &llm.ChatResponse{}
	toolByBlockIndex := map[int]int{}
	currentEvent := ""
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		if currentEvent == "ping" {
			continue
		}

		var ev anthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return nil, fmt.Errorf("failed to parse stream event %q: %w", currentEvent, err)
		}

		switch currentEvent {
		case "message_start":
			result.Usage.InputTokens = ev.Usage.InputTokens
			result.Usage.OutputTokens = ev.Usage.OutputTokens
			if onEvent != nil {
				if err := onEvent(llm.StreamEvent{Type: llm.StreamEventUsage, Usage: result.Usage}); err != nil {
					return nil, err
				}
			}
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
					ID:   ev.ContentBlock.ID,
					Name: ev.ContentBlock.Name,
				})
				toolByBlockIndex[ev.Index] = len(result.ToolCalls) - 1
			}
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				result.Content += ev.Delta.Text
				if onEvent != nil {
					if err := onEvent(llm.StreamEvent{
						Type:         llm.StreamEventContentDelta,
						ContentDelta: ev.Delta.Text,
					}); err != nil {
						return nil, err
					}
				}
			}
			if ev.Delta.Type == "input_json_delta" && ev.Delta.PartialJSON != "" {
				if tcIdx, ok := toolByBlockIndex[ev.Index]; ok {
					result.ToolCalls[tcIdx].Input += ev.Delta.PartialJSON
					if onEvent != nil {
						tc := result.ToolCalls[tcIdx]
						if err := onEvent(llm.StreamEvent{
							Type:           llm.StreamEventToolCallDelta,
							ToolCallIndex:  ev.Index,
							ToolCallID:     tc.ID,
							ToolCallName:   tc.Name,
							ToolInputDelta: ev.Delta.PartialJSON,
						}); err != nil {
							return nil, err
						}
					}
				}
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				result.StopReason = ev.Delta.StopReason
			}
			if ev.Usage.OutputTokens > 0 || ev.Usage.InputTokens > 0 {
				if ev.Usage.InputTokens > 0 {
					result.Usage.InputTokens = ev.Usage.InputTokens
				}
				result.Usage.OutputTokens = ev.Usage.OutputTokens
				if onEvent != nil {
					if err := onEvent(llm.StreamEvent{Type: llm.StreamEventUsage, Usage: result.Usage}); err != nil {
						return nil, err
					}
				}
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

// convertMessage converts an LLM message to Anthropic format
func (c *Client) convertMessage(msg llm.Message) anthropicMessage {
	if msg.Role == "tool" {
		// Tool results need special handling
		blocks := make([]contentBlock, 0, len(msg.ToolResults))
		for _, result := range msg.ToolResults {
			// Ensure ToolCallID is not empty to avoid tool_use_id validation errors
			if result.ToolCallID == "" {
				continue // Skip tool results without valid tool call IDs
			}
			
			content := any(result.Content)
			if inline := extractInlineImage(result.Metadata); inline != nil {
				content = []map[string]interface{}{
					{
						"type": "image",
						"source": map[string]interface{}{
							"type":       "base64",
							"media_type": inline.MediaType,
							"data":       inline.DataBase64,
						},
					},
					{
						"type": "text",
						"text": result.Content,
					},
				}
			}
			blocks = append(blocks, contentBlock{
				Type:      "tool_result",
				ToolUseID: result.ToolCallID,
				Content:   content,
				IsError:   result.IsError,
			})
		}
		
		// Only create tool result message if we have valid blocks
		if len(blocks) > 0 {
			return anthropicMessage{
				Role:    "user",
				Content: blocks,
			}
		}
		
		// If no valid tool results, return empty user message
		return anthropicMessage{
			Role:    "user",
			Content: "",
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
			// Skip tool calls with missing required fields
			if tc.ID == "" || tc.Name == "" {
				logging.Debug("Skipping tool call with missing ID or Name: ID=%s, Name=%s", tc.ID, tc.Name)
				continue
			}
			
			var input any
			if tc.Input != "" {
				if err := json.Unmarshal([]byte(tc.Input), &input); err != nil {
					// If input is malformed, use empty object instead of nil
					logging.Debug("Fixed malformed tool call input for %s: %v", tc.Name, err)
					input = map[string]interface{}{}
				}
			} else {
				// If input is empty, use empty object
				logging.Debug("Fixed empty tool call input for %s", tc.Name)
				input = map[string]interface{}{}
			}
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

type inlineImage struct {
	MediaType  string
	DataBase64 string
}

func extractInlineImage(metadata map[string]interface{}) *inlineImage {
	if len(metadata) == 0 {
		return nil
	}
	rawInline, ok := metadata["image_inline"]
	if !ok {
		return nil
	}
	inlineMap, ok := rawInline.(map[string]interface{})
	if !ok {
		return nil
	}
	mediaType, _ := inlineMap["media_type"].(string)
	dataBase64, _ := inlineMap["data_base64"].(string)
	mediaType = strings.TrimSpace(mediaType)
	dataBase64 = strings.TrimSpace(dataBase64)
	if mediaType == "" {
		return nil
	}
	if dataBase64 == "" {
		path := strings.TrimSpace(asString(inlineMap["path"]))
		if path == "" {
			path = strings.TrimSpace(asString(metadataPath(metadata)))
		}
		if path == "" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			return nil
		}
		maxBytes := int64(2 * 1024 * 1024)
		if v, ok := inlineMap["max_bytes"].(float64); ok && int64(v) > 0 {
			maxBytes = int64(v)
		}
		if int64(len(raw)) > maxBytes {
			return nil
		}
		dataBase64 = base64.StdEncoding.EncodeToString(raw)
	}
	if dataBase64 == "" {
		return nil
	}
	return &inlineImage{
		MediaType:  mediaType,
		DataBase64: dataBase64,
	}
}

func metadataPath(metadata map[string]interface{}) string {
	imageFile, ok := metadata["image_file"]
	if !ok {
		return ""
	}
	imageFileMap, ok := imageFile.(map[string]interface{})
	if !ok {
		return ""
	}
	return asString(imageFileMap["path"])
}

func asString(value interface{}) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

// Ensure Client implements llm.Client
var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
