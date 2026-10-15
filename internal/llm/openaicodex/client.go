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
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const (
	defaultBaseURL               = "https://chatgpt.com/backend-api/codex"
	defaultRequestTimeout        = 10 * time.Minute
	defaultResponseHeaderTimeout = 60 * time.Second
	codexPrefix                  = "You are Codex, based on GPT-5. You are running as a coding agent in a local CLI."
)

type Client struct {
	accessToken string
	baseURL     string
	model       string
	accountID   string
	options     Options
	tokenMu     sync.Mutex
	httpClient  *http.Client
}

type Options struct {
	// PublicAPI disables ChatGPT-only headers and payload restrictions.
	PublicAPI       bool
	PromptCacheKey  string
	ReasoningEffort string
	TextVerbosity   string
	ServiceTier     string
	// Token caps are supported only by the public Responses API.
	MaxTokens             int
	StatefulResponses     bool
	ResponseHeaderTimeout time.Duration
	AccessTokenProvider   func() string
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
	Include            []string             `json:"include,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type responsesText struct {
	Verbosity string `json:"verbosity,omitempty"`
}

type responsesInputItem struct {
	ID               string                 `json:"id,omitempty"`
	EncryptedContent string                 `json:"encrypted_content,omitempty"`
	Summary          json.RawMessage        `json:"summary,omitempty"`
	Type             string                 `json:"type,omitempty"`
	Role             string                 `json:"role,omitempty"`
	Content          []responsesInputPart   `json:"content,omitempty"`
	CallID           string                 `json:"call_id,omitempty"`
	Name             string                 `json:"name,omitempty"`
	Arguments        string                 `json:"arguments,omitempty"`
	Output           *string                `json:"output,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
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
	Strict      *bool                  `json:"strict,omitempty"`
}

type responsesResponse struct {
	ID                string                `json:"id"`
	Status            string                `json:"status"`
	Output            []responsesOutputItem `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage struct {
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
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	CallID           string          `json:"call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Arguments        string          `json:"arguments,omitempty"`
	Role             string          `json:"role,omitempty"`
	Content          []struct {
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
		if options.PublicAPI {
			normalizedBaseURL = "https://api.openai.com/v1"
		}
	}
	normalizedBaseURL = strings.TrimRight(normalizedBaseURL, "/")
	for _, suffix := range []string{"/responses", "/chat/completions", "/models"} {
		normalizedBaseURL = strings.TrimSuffix(normalizedBaseURL, suffix)
	}
	options = normalizeOptions(options)
	return &Client{
		accessToken: strings.TrimSpace(accessToken),
		baseURL:     normalizedBaseURL,
		model:       NormalizeModelID(model),
		accountID:   extractAccountID(strings.TrimSpace(accessToken)),
		options:     options,
		httpClient:  newHTTPClient(options.ResponseHeaderTimeout),
	}
}

func (c *Client) currentAccessToken() string {
	if c == nil {
		return ""
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	accessToken := strings.TrimSpace(c.accessToken)
	if c.options.AccessTokenProvider != nil {
		// WHY: Codex CLI refreshes its OAuth cache independently of this long-lived
		// client. Re-read the token lazily so active Brute sessions do not keep a
		// stale bearer after ~/.codex/auth.json changes.
		if fresh := strings.TrimSpace(c.options.AccessTokenProvider()); fresh != "" {
			accessToken = fresh
			c.accessToken = fresh
			c.accountID = extractAccountID(fresh)
		}
	}
	return accessToken
}

func newHTTPClient(responseHeaderTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return &http.Client{
		Timeout:   defaultRequestTimeout,
		Transport: transport,
	}
}

func (c *Client) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	return c.ChatStream(ctx, request, nil)
}

func (c *Client) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	if request == nil {
		request = &llm.ChatRequest{}
	}
	model := NormalizeModelID(request.Model)
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
	if options.PublicAPI {
		// Brute tools have optional parameters; automatic strict normalization
		// would turn those into required fields and change their semantics.
		strict := false
		for i := range tools {
			tools[i].Strict = &strict
		}
	}
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
	}
	if effort := strings.TrimSpace(options.ReasoningEffort); effort != "" {
		payload.Reasoning = &responsesReasoning{Effort: effort}
	}
	if verbosity := strings.TrimSpace(options.TextVerbosity); verbosity != "" {
		payload.Text = &responsesText{Verbosity: verbosity}
	}

	if options.PublicAPI {
		payload.Instructions = strings.TrimSpace(request.SystemPrompt)
		payload.MaxOutputTokens = options.MaxTokens
		if !options.StatefulResponses {
			// Encrypted items preserve reasoning without storing responses at OpenAI.
			payload.Include = []string{"reasoning.encrypted_content"}
		}
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
	req.Header.Set("Accept", "text/event-stream")
	if !options.PublicAPI {
		req.Header.Set("Originator", "codex_cli_rs")
		req.Header.Set("User-Agent", UserAgent())
	}
	accessToken := c.currentAccessToken()
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	if !options.PublicAPI && c.accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", c.accountID)
	}

	resp, err := llm.DoStream(c.httpClient, req)
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
		if options.PublicAPI && options.ServiceTier == "flex" && resp.StatusCode == http.StatusTooManyRequests && ctx.Err() == nil {
			// Retry before the generic retry wrapper can repeat the unavailable
			// flex request. Empty tier would inherit a provider's flex default.
			logging.Warn("OpenAI flex unavailable; retrying once with auto service tier")
			_ = resp.Body.Close()
			retryRequest := *request
			retryRequest.ServiceTier = "auto"
			result, retryErr := c.ChatStream(ctx, &retryRequest, onEvent)
			return result, llm.UnsafeForRetry(retryErr)
		}
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
	result, err := parseResponseObject(parsed)
	if err != nil {
		return nil, err
	}
	return result, incompleteResponseError(parsed, result)
}

func incompleteResponseError(parsed responsesResponse, result *llm.ChatResponse) error {
	if parsed.Status != "incomplete" || strings.TrimSpace(result.Content) != "" || len(result.ToolCalls) > 0 {
		return nil
	}
	reason := "unknown"
	if parsed.IncompleteDetails != nil && parsed.IncompleteDetails.Reason != "" {
		reason = parsed.IncompleteDetails.Reason
	}
	return fmt.Errorf("OpenAI response incomplete with no content (reason: %s)", reason)
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
		case "response.completed", "response.incomplete", "response.failed":
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
		return result, incompleteResponseError(*completed, result)
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
		case "reasoning":
			if item.EncryptedContent != "" {
				raw, err := json.Marshal(responsesInputItem{
					Type: "reasoning", ID: item.ID,
					EncryptedContent: item.EncryptedContent, Summary: item.Summary,
				})
				if err != nil {
					return nil, fmt.Errorf("failed to preserve reasoning item: %w", err)
				}
				result.ReasoningItems = append(result.ReasoningItems, raw)
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
	result.Content = contentBuilder.String()

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
				output := tr.Content
				items = append(items, responsesInputItem{
					Type:   "function_call_output",
					CallID: strings.TrimSpace(tr.ToolCallID),
					Output: &output,
				})
			}
		case "assistant":
			for _, raw := range msg.ReasoningItems {
				var item responsesInputItem
				if json.Unmarshal(raw, &item) == nil && item.Type == "reasoning" {
					items = append(items, item)
				}
			}
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

var _ llm.Client = (*Client)(nil)
var _ llm.StreamingClient = (*Client)(nil)
