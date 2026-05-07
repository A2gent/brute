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
	"os"
	"strconv"
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
	options     Options
	httpClient  *http.Client
}

type Options struct {
	PromptCacheKey    string
	ReasoningEffort   string
	TextVerbosity     string
	ServiceTier       string
	MaxTokens         int
	StatefulResponses bool
}

type responsesRequest struct {
	Model              string               `json:"model"`
	Instructions       string               `json:"instructions,omitempty"`
	Input              []responsesInputItem `json:"input"`
	Tools              []responsesTool      `json:"tools,omitempty"`
	Store              bool                 `json:"store"`
	Stream             bool                 `json:"stream"`
	PreviousResponseID string               `json:"previous_response_id,omitempty"`
	PromptCacheKey     string               `json:"prompt_cache_key,omitempty"`
	Reasoning          *responsesReasoning  `json:"reasoning,omitempty"`
	Text               *responsesText       `json:"text,omitempty"`
	ServiceTier        string               `json:"service_tier,omitempty"`
	MaxOutputTokens    int                  `json:"max_output_tokens,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type responsesText struct {
	Verbosity string `json:"verbosity,omitempty"`
}

type responsesInputItem struct {
	Type      string                 `json:"type,omitempty"`
	Role      string                 `json:"role,omitempty"`
	Content   []responsesInputPart   `json:"content,omitempty"`
	CallID    string                 `json:"call_id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Arguments string                 `json:"arguments,omitempty"`
	Output    *string                `json:"output,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

type responsesInputPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
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
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		InputTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
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
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		ImageURL string `json:"image_url,omitempty"`
	} `json:"content,omitempty"`
}

type streamToolState struct {
	id    string
	name  string
	input strings.Builder
}

func NewClient(accessToken, model, baseURL string) *Client {
	return NewClientWithOptions(accessToken, model, baseURL, Options{
		PromptCacheKey:    strings.TrimSpace(os.Getenv("AAGENT_OPENAI_CODEX_PROMPT_CACHE_KEY")),
		ReasoningEffort:   strings.TrimSpace(os.Getenv("AAGENT_OPENAI_CODEX_REASONING_EFFORT")),
		TextVerbosity:     strings.TrimSpace(os.Getenv("AAGENT_OPENAI_CODEX_TEXT_VERBOSITY")),
		ServiceTier:       strings.TrimSpace(os.Getenv("AAGENT_OPENAI_CODEX_SERVICE_TIER")),
		MaxTokens:         envInt("AAGENT_OPENAI_CODEX_MAX_TOKENS"),
		StatefulResponses: envBool("AAGENT_OPENAI_CODEX_STATEFUL_RESPONSES"),
	})
}

func NewClientWithOptions(accessToken, model, baseURL string, options Options) *Client {
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
		options:     normalizeOptions(options),
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}
}

func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	return c.ChatStream(ctx, request, nil)
}

func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	if request == nil {
		request = &llm.ChatRequest{}
	}
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

	options := c.requestOptions(request)
	payload := responsesRequest{
		Model:              model,
		Instructions:       codexInstructions(request.SystemPrompt),
		Input:              input,
		Tools:              tools,
		Store:              options.StatefulResponses,
		Stream:             true,
		PreviousResponseID: options.previousResponseID(request),
		PromptCacheKey:     options.promptCacheKey(request),
		ServiceTier:        options.ServiceTier,
		MaxOutputTokens:    options.maxTokens(request),
	}
	if effort := strings.TrimSpace(options.ReasoningEffort); effort != "" {
		payload.Reasoning = &responsesReasoning{Effort: effort}
	}
	if verbosity := strings.TrimSpace(options.TextVerbosity); verbosity != "" {
		payload.Text = &responsesText{Verbosity: verbosity}
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

	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("failed to read Codex error response: %w", readErr)
		}
		err := fmt.Errorf("OpenAI Codex error (%d): %s", resp.StatusCode, string(respBody))
		logging.LogResponse(0, 0, 0, err)
		return nil, err
	}

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if strings.Contains(contentType, "text/event-stream") {
		return parseStreamReader(resp.Body, onEvent)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Codex response: %w", err)
	}
	trimmedBody := strings.TrimSpace(string(respBody))
	if strings.HasPrefix(trimmedBody, "event:") || strings.HasPrefix(trimmedBody, "data:") {
		return parseStreamResponse(respBody)
	}
	var parsed responsesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse Codex response: %w", err)
	}
	return parseResponseObject(parsed)
}

func parseStreamResponse(body []byte) (*llm.ChatResponse, error) {
	return parseStreamReader(bytes.NewReader(body), nil)
}

func parseStreamReader(reader io.Reader, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	var completed *responsesResponse
	streamedItems := make([]responsesOutputItem, 0)
	var textFallback strings.Builder
	tools := map[string]*streamToolState{}
	toolOrder := make([]string, 0)

	ensureToolState := func(id string) *streamToolState {
		state := tools[id]
		if state != nil {
			return state
		}
		state = &streamToolState{id: id}
		tools[id] = state
		toolOrder = append(toolOrder, id)
		return state
	}

	scanner := bufio.NewScanner(reader)
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
			if onEvent != nil && ev.Delta != "" {
				if err := onEvent(llm.StreamEvent{
					Type:         llm.StreamEventContentDelta,
					ContentDelta: ev.Delta,
				}); err != nil {
					return nil, err
				}
			}
		case "response.function_call_arguments.delta":
			id := strings.TrimSpace(ev.ItemID)
			if id == "" {
				id = fmt.Sprintf("tool_%d", ev.OutputIx)
			}
			state := ensureToolState(id)
			state.input.WriteString(ev.Delta)
			if onEvent != nil && ev.Delta != "" {
				if err := onEvent(llm.StreamEvent{
					Type:           llm.StreamEventToolCallDelta,
					ToolCallIndex:  ev.OutputIx,
					ToolCallID:     id,
					ToolCallName:   state.name,
					ToolInputDelta: ev.Delta,
				}); err != nil {
					return nil, err
				}
			}
		case "response.output_item.done":
			if len(ev.Item) == 0 {
				continue
			}
			var item responsesOutputItem
			if err := json.Unmarshal(ev.Item, &item); err != nil {
				continue
			}
			streamedItems = append(streamedItems, item)
			if item.Type == "function_call" {
				id := strings.TrimSpace(item.CallID)
				if id == "" {
					id = strings.TrimSpace(item.ID)
				}
				if id == "" {
					id = fmt.Sprintf("tool_%d", ev.OutputIx)
				}
				state := ensureToolState(id)
				if strings.TrimSpace(item.Name) != "" {
					state.name = strings.TrimSpace(item.Name)
				}
				if strings.TrimSpace(item.Arguments) != "" && state.input.Len() == 0 {
					state.input.WriteString(strings.TrimSpace(item.Arguments))
				}
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
		if len(completed.Output) == 0 && len(streamedItems) > 0 {
			completed.Output = append(completed.Output, streamedItems...)
		}
		result, err := parseResponseObject(*completed)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(result.Content) == "" {
			result.Content = strings.TrimSpace(textFallback.String())
		}
		if len(result.ToolCalls) == 0 && len(toolOrder) > 0 {
			result.ToolCalls = toolCallsFromStates(toolOrder, tools)
		}
		return result, nil
	}

	result := &llm.ChatResponse{
		Content: strings.TrimSpace(textFallback.String()),
	}
	result.ToolCalls = toolCallsFromStates(toolOrder, tools)
	return result, nil
}

func toolCallsFromStates(order []string, tools map[string]*streamToolState) []llm.ToolCall {
	result := make([]llm.ToolCall, 0, len(order))
	for _, id := range order {
		state := tools[id]
		if state == nil || strings.TrimSpace(state.name) == "" {
			continue
		}
		result = append(result, llm.ToolCall{
			ID:    state.id,
			Name:  state.name,
			Input: strings.TrimSpace(state.input.String()),
		})
	}
	return result
}

func parseResponseObject(parsed responsesResponse) (*llm.ChatResponse, error) {
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return nil, fmt.Errorf("OpenAI Codex error: %s", parsed.Error.Message)
	}

	result := &llm.ChatResponse{
		StopReason: parsed.Status,
		ResponseID: strings.TrimSpace(parsed.ID),
		Usage: llm.TokenUsage{
			InputTokens:       parsed.Usage.InputTokens,
			OutputTokens:      parsed.Usage.OutputTokens,
			CachedInputTokens: parsed.Usage.InputTokensDetails.CachedTokens,
			ReasoningTokens:   parsed.Usage.OutputTokensDetails.ReasoningTokens,
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
				case "output_image", "image":
					imageURL := strings.TrimSpace(block.ImageURL)
					if imageURL != "" {
						result.Images = append(result.Images, llm.Image{URL: imageURL})
					}
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
				output := strings.TrimSpace(tr.Content)
				items = append(items, responsesInputItem{
					Type:   "function_call_output",
					CallID: strings.TrimSpace(tr.ToolCallID),
					Output: &output,
				})
			}
		case "assistant":
			if content != "" {
				items = append(items, responsesInputItem{
					Type: "message",
					Role: "assistant",
					Content: []responsesInputPart{
						{Type: "output_text", Text: content},
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
			if content == "" && len(msg.Images) == 0 {
				continue
			}
			parts := make([]responsesInputPart, 0, len(msg.Images)+1)
			if content != "" {
				parts = append(parts, responsesInputPart{Type: "input_text", Text: content})
			}
			for _, img := range msg.Images {
				url := strings.TrimSpace(img.URL)
				if url == "" {
					url = img.DataURL()
				}
				if url == "" {
					continue
				}
				parts = append(parts, responsesInputPart{Type: "input_image", ImageURL: url})
			}
			if len(parts) == 0 {
				continue
			}
			items = append(items, responsesInputItem{
				Type:    "message",
				Role:    role,
				Content: parts,
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

func normalizeOptions(options Options) Options {
	options.PromptCacheKey = strings.TrimSpace(options.PromptCacheKey)
	options.ReasoningEffort = strings.TrimSpace(options.ReasoningEffort)
	options.TextVerbosity = strings.TrimSpace(options.TextVerbosity)
	options.ServiceTier = strings.TrimSpace(options.ServiceTier)
	// The ChatGPT Codex backend rejects stored responses.
	options.StatefulResponses = false
	if options.MaxTokens < 0 {
		options.MaxTokens = 0
	}
	return options
}

func (c *Client) requestOptions(request *llm.ChatRequest) Options {
	options := c.options
	if request == nil {
		return options
	}
	if strings.TrimSpace(request.PromptCacheKey) != "" {
		options.PromptCacheKey = strings.TrimSpace(request.PromptCacheKey)
	}
	if strings.TrimSpace(request.ReasoningEffort) != "" {
		options.ReasoningEffort = strings.TrimSpace(request.ReasoningEffort)
	}
	if strings.TrimSpace(request.TextVerbosity) != "" {
		options.TextVerbosity = strings.TrimSpace(request.TextVerbosity)
	}
	if strings.TrimSpace(request.ServiceTier) != "" {
		options.ServiceTier = strings.TrimSpace(request.ServiceTier)
	}
	if request.MaxTokens > 0 {
		options.MaxTokens = request.MaxTokens
	}
	return normalizeOptions(options)
}

func (options Options) promptCacheKey(request *llm.ChatRequest) string {
	if key := strings.TrimSpace(options.PromptCacheKey); key != "" {
		return key
	}
	if request != nil {
		return strings.TrimSpace(request.SessionID)
	}
	return ""
}

func (options Options) previousResponseID(request *llm.ChatRequest) string {
	if !options.StatefulResponses || request == nil {
		return ""
	}
	return strings.TrimSpace(request.PreviousResponseID)
}

func (options Options) maxTokens(request *llm.ChatRequest) int {
	if options.MaxTokens > 0 {
		return options.MaxTokens
	}
	if request != nil && request.MaxTokens > 0 {
		return request.MaxTokens
	}
	return 0
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt(key string) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
