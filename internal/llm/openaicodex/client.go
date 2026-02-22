package openaicodex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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
	defaultBaseURL = "https://chatgpt.com/backend-api/codex"
	codexPrefix    = "You are Codex, based on GPT-5. You are running as a coding agent in a local CLI."
)

type Client struct {
	accessToken string
	baseURL     string
	model       string
	accountID   string
	httpClient  *http.Client
}

type responsesRequest struct {
	Model        string               `json:"model"`
	Instructions string               `json:"instructions,omitempty"`
	Input        []responsesInputItem `json:"input"`
	Tools        []responsesTool      `json:"tools,omitempty"`
	Store        bool                 `json:"store"`
	Stream       bool                 `json:"stream"`
}

type responsesInputItem struct {
	Type      string                 `json:"type,omitempty"`
	Role      string                 `json:"role,omitempty"`
	Content   []responsesInputText   `json:"content,omitempty"`
	CallID    string                 `json:"call_id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Arguments string                 `json:"arguments,omitempty"`
	Output    string                 `json:"output,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

type responsesInputText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesTool struct {
	Type        string                 `json:"type"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type responsesResponse struct {
	ID     string                `json:"id"`
	Status string                `json:"status"`
	Output []responsesOutputItem `json:"output"`
	Usage  struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

type responsesOutputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Role      string `json:"role,omitempty"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"content,omitempty"`
}

func NewClient(accessToken, model, baseURL string) *Client {
	normalizedBaseURL := strings.TrimSpace(baseURL)
	if normalizedBaseURL == "" {
		normalizedBaseURL = defaultBaseURL
	}
	normalizedBaseURL = strings.TrimRight(normalizedBaseURL, "/")
	if strings.HasSuffix(normalizedBaseURL, "/responses") {
		normalizedBaseURL = strings.TrimSuffix(normalizedBaseURL, "/responses")
	}
	return &Client{
		accessToken: strings.TrimSpace(accessToken),
		baseURL:     normalizedBaseURL,
		model:       strings.TrimSpace(model),
		accountID:   extractAccountID(strings.TrimSpace(accessToken)),
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}
}

func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = c.model
	}
	if model == "" {
		return nil, fmt.Errorf("OpenAI Codex model is not configured")
	}

	lastMsg := ""
	if len(request.Messages) > 0 {
		lastMsg = request.Messages[len(request.Messages)-1].Content
	}
	logging.LogRequestWithContent(model, len(request.Messages), len(request.Tools) > 0, lastMsg)

	input := buildInputItems(request.Messages)
	tools := make([]responsesTool, 0, len(request.Tools))
	for _, t := range request.Tools {
		tools = append(tools, responsesTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}

	payload := responsesRequest{
		Model:        model,
		Instructions: codexInstructions(request.SystemPrompt),
		Input:        input,
		Tools:        tools,
		Store:        false,
		Stream:       true,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Codex request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create Codex request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Originator", "a2gent")
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}
	if c.accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", c.accountID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Codex response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("OpenAI Codex error (%d): %s", resp.StatusCode, string(respBody))
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	trimmedBody := strings.TrimSpace(string(respBody))
	if strings.Contains(contentType, "text/event-stream") ||
		strings.HasPrefix(trimmedBody, "event:") ||
		strings.HasPrefix(trimmedBody, "data:") {
		return parseStreamResponse(respBody)
	}

	var parsed responsesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse Codex response: %w", err)
	}
	return parseResponseObject(parsed)
}

func parseStreamResponse(body []byte) (*llm.ChatResponse, error) {
	var completed *responsesResponse
	var textFallback strings.Builder
	type toolState struct {
		id    string
		name  string
		input strings.Builder
	}
	tools := map[string]*toolState{}

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var ev struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta,omitempty"`
			ItemID   string          `json:"item_id,omitempty"`
			OutputIx int             `json:"output_index,omitempty"`
			Response json.RawMessage `json:"response,omitempty"`
			Item     json.RawMessage `json:"item,omitempty"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "response.output_text.delta":
			textFallback.WriteString(ev.Delta)
		case "response.function_call_arguments.delta":
			id := strings.TrimSpace(ev.ItemID)
			if id == "" {
				id = fmt.Sprintf("tool_%d", ev.OutputIx)
			}
			state := tools[id]
			if state == nil {
				state = &toolState{id: id}
				tools[id] = state
			}
			state.input.WriteString(ev.Delta)
		case "response.output_item.done":
			if len(ev.Item) == 0 {
				continue
			}
			var item responsesOutputItem
			if err := json.Unmarshal(ev.Item, &item); err != nil {
				continue
			}
			if item.Type != "function_call" {
				continue
			}
			id := strings.TrimSpace(item.CallID)
			if id == "" {
				id = strings.TrimSpace(item.ID)
			}
			if id == "" {
				id = fmt.Sprintf("tool_%d", ev.OutputIx)
			}
			state := tools[id]
			if state == nil {
				state = &toolState{id: id}
				tools[id] = state
			}
			if strings.TrimSpace(item.Name) != "" {
				state.name = strings.TrimSpace(item.Name)
			}
			if strings.TrimSpace(item.Arguments) != "" && state.input.Len() == 0 {
				state.input.WriteString(strings.TrimSpace(item.Arguments))
			}
		case "response.completed":
			if len(ev.Response) == 0 {
				continue
			}
			var parsed responsesResponse
			if err := json.Unmarshal(ev.Response, &parsed); err == nil {
				completed = &parsed
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read Codex stream: %w", err)
	}

	if completed != nil {
		return parseResponseObject(*completed)
	}

	result := &llm.ChatResponse{
		Content: strings.TrimSpace(textFallback.String()),
	}
	for _, state := range tools {
		if strings.TrimSpace(state.name) == "" {
			continue
		}
		result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
			ID:    state.id,
			Name:  state.name,
			Input: strings.TrimSpace(state.input.String()),
		})
	}
	return result, nil
}

func parseResponseObject(parsed responsesResponse) (*llm.ChatResponse, error) {
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return nil, fmt.Errorf("OpenAI Codex error: %s", parsed.Error.Message)
	}

	result := &llm.ChatResponse{
		StopReason: parsed.Status,
		Usage: llm.TokenUsage{
			InputTokens:  parsed.Usage.InputTokens,
			OutputTokens: parsed.Usage.OutputTokens,
		},
	}

	var contentBuilder strings.Builder
	for _, item := range parsed.Output {
		switch item.Type {
		case "message":
			for _, block := range item.Content {
				switch block.Type {
				case "output_text", "text", "input_text":
					contentBuilder.WriteString(block.Text)
				}
			}
		case "function_call":
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				callID = strings.TrimSpace(item.ID)
			}
			result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
				ID:    callID,
				Name:  strings.TrimSpace(item.Name),
				Input: strings.TrimSpace(item.Arguments),
			})
		}
	}
	result.Content = strings.TrimSpace(contentBuilder.String())

	toolNames := make([]string, len(result.ToolCalls))
	for i, tc := range result.ToolCalls {
		toolNames[i] = tc.Name
	}
	logging.LogResponseWithContent(result.Usage.InputTokens, result.Usage.OutputTokens, len(result.ToolCalls), result.Content, toolNames)

	return result, nil
}

func buildInputItems(messages []llm.Message) []responsesInputItem {
	items := make([]responsesInputItem, 0, len(messages))
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		content := strings.TrimSpace(msg.Content)
		switch role {
		case "tool":
			for _, tr := range msg.ToolResults {
				items = append(items, responsesInputItem{
					Type:   "function_call_output",
					CallID: strings.TrimSpace(tr.ToolCallID),
					Output: strings.TrimSpace(tr.Content),
				})
			}
		case "assistant":
			if content != "" {
				items = append(items, responsesInputItem{
					Type: "message",
					Role: "assistant",
					Content: []responsesInputText{
						{Type: "input_text", Text: content},
					},
				})
			}
			for _, tc := range msg.ToolCalls {
				items = append(items, responsesInputItem{
					Type:      "function_call",
					CallID:    strings.TrimSpace(tc.ID),
					Name:      strings.TrimSpace(tc.Name),
					Arguments: strings.TrimSpace(tc.Input),
				})
			}
		default:
			if content == "" {
				continue
			}
			items = append(items, responsesInputItem{
				Type: "message",
				Role: role,
				Content: []responsesInputText{
					{Type: "input_text", Text: content},
				},
			})
		}
	}
	return items
}

func extractAccountID(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payloadRaw, &claims); err != nil {
		return ""
	}

	if id := stringClaim(claims["chatgpt_account_id"]); id != "" {
		return id
	}
	if raw, ok := claims["https://api.openai.com/auth"].(map[string]interface{}); ok {
		if id := stringClaim(raw["chatgpt_account_id"]); id != "" {
			return id
		}
	}
	if orgs, ok := claims["organizations"].([]interface{}); ok {
		for _, org := range orgs {
			if orgMap, ok := org.(map[string]interface{}); ok {
				if id := stringClaim(orgMap["id"]); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

func stringClaim(v interface{}) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func codexInstructions(systemPrompt string) string {
	prompt := strings.TrimSpace(systemPrompt)
	if prompt == "" {
		return codexPrefix
	}
	return codexPrefix + "\n\n" + prompt
}

var _ llm.Client = (*Client)(nil)
