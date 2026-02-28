package http

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type proxyModelsResponse struct {
	Object string           `json:"object"`
	Data   []proxyModelInfo `json:"data"`
}

type proxyModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type proxyChatRequest struct {
	Model       string           `json:"model"`
	Provider    string           `json:"provider,omitempty"`
	Messages    []proxyMessage   `json:"messages"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
	Tools       []proxyTool      `json:"tools,omitempty"`
	ToolChoice  json.RawMessage  `json:"tool_choice,omitempty"`
	Metadata    json.RawMessage  `json:"metadata,omitempty"`
	ExtraBody   *json.RawMessage `json:"extra_body,omitempty"`
}

type proxyMessage struct {
	Role       string          `json:"role"`
	Content    any             `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []proxyToolCall `json:"tool_calls,omitempty"`
}

type proxyToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type proxyTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		Parameters  map[string]interface{} `json:"parameters"`
	} `json:"function"`
}

type proxyChatResponse struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []proxyChoice       `json:"choices"`
	Usage   proxyUsage          `json:"usage"`
	XA2gent *proxyRoutingDetail `json:"x_a2gent,omitempty"`
}

type proxyChoice struct {
	Index        int          `json:"index"`
	Message      proxyMessage `json:"message"`
	FinishReason string       `json:"finish_reason"`
}

type proxyUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type proxyRoutingDetail struct {
	Provider string `json:"provider"`
}

type proxyChatStreamChunk struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []proxyChatStreamDelta `json:"choices"`
}

type proxyChatStreamDelta struct {
	Index        int                    `json:"index"`
	Delta        proxyChatMessageDelta  `json:"delta"`
	FinishReason *string                `json:"finish_reason"`
	Logprobs     map[string]interface{} `json:"logprobs,omitempty"`
}

type proxyChatMessageDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolCalls []proxyToolCall `json:"tool_calls,omitempty"`
}

func (s *Server) llmProxyEnabled() bool {
	settings, err := s.store.GetSettings()
	if err != nil || settings == nil {
		return true
	}
	raw := strings.TrimSpace(strings.ToLower(settings[llmProviderProxyEnabledSettingKey]))
	switch raw {
	case "", "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *Server) handleLLMProxyModels(w http.ResponseWriter, r *http.Request) {
	if !s.llmProxyEnabled() {
		s.errorResponse(w, http.StatusNotFound, "LLM provider proxy is disabled")
		return
	}

	models := s.proxyModelCatalog("")
	s.jsonResponse(w, http.StatusOK, proxyModelsResponse{
		Object: "list",
		Data:   models,
	})
}

func (s *Server) handleLLMProxyProviderModels(w http.ResponseWriter, r *http.Request) {
	if !s.llmProxyEnabled() {
		s.errorResponse(w, http.StatusNotFound, "LLM provider proxy is disabled")
		return
	}

	providerRef := config.NormalizeProviderRef(chi.URLParam(r, "providerRef"))
	if providerRef == "" {
		s.errorResponse(w, http.StatusBadRequest, "providerRef is required")
		return
	}
	if err := s.validateProviderRefForExecution(providerRef); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Unsupported provider: "+providerRef)
		return
	}

	models := s.proxyModelCatalog(providerRef)
	s.jsonResponse(w, http.StatusOK, proxyModelsResponse{
		Object: "list",
		Data:   models,
	})
}

func (s *Server) proxyModelCatalog(scopedProviderRef string) []proxyModelInfo {
	seen := make(map[string]struct{})
	out := make([]proxyModelInfo, 0, 16)
	appendModel := func(id, owner string) {
		modelID := strings.TrimSpace(id)
		if modelID == "" {
			return
		}
		if _, ok := seen[modelID]; ok {
			return
		}
		seen[modelID] = struct{}{}
		out = append(out, proxyModelInfo{
			ID:      modelID,
			Object:  "model",
			OwnedBy: owner,
		})
	}

	if scopedProviderRef != "" {
		providerType := config.ProviderType(scopedProviderRef)
		for _, model := range s.proxyModelNamesForProvider(providerType) {
			appendModel(model, scopedProviderRef)
		}
		return out
	}

	activeProvider := config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
	activeModel := strings.TrimSpace(s.resolveModelForProvider(activeProvider))
	if activeModel != "" {
		appendModel(activeModel, "a2gent-active")
	}

	for _, def := range config.SupportedProviders() {
		ptype := def.Type
		if ptype == config.ProviderFallback || ptype == config.ProviderAutoRouter {
			continue
		}
		if !s.providerConfiguredForUse(ptype) {
			continue
		}
		ref := string(ptype)
		for _, model := range s.proxyModelNamesForProvider(ptype) {
			appendModel(fmt.Sprintf("%s/%s", ref, model), ref)
		}
	}

	for _, aggregate := range s.config.FallbackAggregates {
		providerRef := config.FallbackAggregateRefFromID(aggregate.ID)
		for _, node := range aggregate.Chain {
			provider := config.NormalizeProviderRef(node.Provider)
			model := strings.TrimSpace(node.Model)
			if provider == "" || model == "" {
				continue
			}
			appendModel(fmt.Sprintf("%s/%s/%s", providerRef, provider, model), providerRef)
		}
	}

	return out
}

func (s *Server) proxyModelNamesForProvider(providerType config.ProviderType) []string {
	seen := make(map[string]struct{})
	models := make([]string, 0, 4)
	add := func(raw string) {
		model := strings.TrimSpace(raw)
		if model == "" {
			return
		}
		if _, ok := seen[model]; ok {
			return
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}

	provider := s.config.Providers[string(providerType)]
	add(provider.Model)
	if def := config.GetProviderDefinition(providerType); def != nil {
		add(def.DefaultModel)
	}
	if resolved := strings.TrimSpace(s.resolveModelForProvider(providerType)); resolved != "" {
		add(resolved)
	}

	return models
}

func (s *Server) handleLLMProxyChatCompletions(w http.ResponseWriter, r *http.Request) {
	s.handleLLMProxyChatCompletionsWithProvider("", w, r)
}

func (s *Server) handleLLMProxyProviderChatCompletions(w http.ResponseWriter, r *http.Request) {
	s.handleLLMProxyChatCompletionsWithProvider(config.NormalizeProviderRef(chi.URLParam(r, "providerRef")), w, r)
}

func (s *Server) handleLLMProxyChatCompletionsWithProvider(providerHint string, w http.ResponseWriter, r *http.Request) {
	if !s.llmProxyEnabled() {
		s.errorResponse(w, http.StatusNotFound, "LLM provider proxy is disabled")
		return
	}

	var req proxyChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "messages are required")
		return
	}

	resolvedProvider, resolvedModel, err := s.resolveProxyProviderAndModel(providerHint, req.Provider, req.Model)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	chatReq, err := buildProxyLLMRequest(&req, resolvedModel)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	client, err := s.createLLMClient(resolvedProvider, resolvedModel, nil)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}

	if req.Stream {
		s.handleLLMProxyChatCompletionsStream(w, r, client, chatReq, resolvedModel)
		return
	}

	resp, err := client.Chat(r.Context(), chatReq)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, proxyChatResponseFromLLM(resp, resolvedProvider, resolvedModel))
}

func (s *Server) handleLLMProxyChatCompletionsStream(
	w http.ResponseWriter,
	r *http.Request,
	client llm.Client,
	chatReq *llm.ChatRequest,
	model string,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.errorResponse(w, http.StatusInternalServerError, "Streaming is not supported by this server")
		return
	}

	requestID := "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	createdAt := time.Now().Unix()
	usage := llm.TokenUsage{}
	finishReason := "stop"

	writeChunk := func(chunk proxyChatStreamChunk) error {
		payload, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte("data: ")); err != nil {
			return err
		}
		if _, err := w.Write(payload); err != nil {
			return err
		}
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	_ = writeChunk(proxyChatStreamChunk{
		ID:      requestID,
		Object:  "chat.completion.chunk",
		Created: createdAt,
		Model:   model,
		Choices: []proxyChatStreamDelta{
			{
				Index: 0,
				Delta: proxyChatMessageDelta{Role: "assistant"},
			},
		},
	})

	streamingClient, supportsStreaming := client.(llm.StreamingClient)
	if !supportsStreaming {
		resp, err := client.Chat(r.Context(), chatReq)
		if err != nil {
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
			return
		}
		contentChunk := proxyChatStreamChunk{
			ID:      requestID,
			Object:  "chat.completion.chunk",
			Created: createdAt,
			Model:   model,
			Choices: []proxyChatStreamDelta{
				{
					Index: 0,
					Delta: proxyChatMessageDelta{
						Role:    "assistant",
						Content: resp.Content,
					},
				},
			},
		}
		_ = writeChunk(contentChunk)
		if len(resp.ToolCalls) > 0 {
			toolCalls := make([]proxyToolCall, 0, len(resp.ToolCalls))
			for _, tc := range resp.ToolCalls {
				item := proxyToolCall{ID: tc.ID, Type: "function"}
				item.Function.Name = tc.Name
				item.Function.Arguments = tc.Input
				toolCalls = append(toolCalls, item)
			}
			_ = writeChunk(proxyChatStreamChunk{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: createdAt,
				Model:   model,
				Choices: []proxyChatStreamDelta{
					{
						Index: 0,
						Delta: proxyChatMessageDelta{
							ToolCalls: toolCalls,
						},
					},
				},
			})
			reason := "tool_calls"
			finishReason = reason
		}
		_ = writeChunk(proxyChatStreamChunk{
			ID:      requestID,
			Object:  "chat.completion.chunk",
			Created: createdAt,
			Model:   model,
			Choices: []proxyChatStreamDelta{
				{
					Index:        0,
					Delta:        proxyChatMessageDelta{},
					FinishReason: &finishReason,
				},
			},
		})
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
		return
	}

	finalResp, streamErr := streamingClient.ChatStream(r.Context(), chatReq, func(event llm.StreamEvent) error {
		switch event.Type {
		case llm.StreamEventContentDelta:
			if strings.TrimSpace(event.ContentDelta) == "" {
				return nil
			}
			return writeChunk(proxyChatStreamChunk{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: createdAt,
				Model:   model,
				Choices: []proxyChatStreamDelta{
					{
						Index: 0,
						Delta: proxyChatMessageDelta{
							Content: event.ContentDelta,
						},
					},
				},
			})
		case llm.StreamEventToolCallDelta:
			tc := proxyToolCall{ID: event.ToolCallID, Type: "function"}
			tc.Function.Name = event.ToolCallName
			tc.Function.Arguments = event.ToolInputDelta
			return writeChunk(proxyChatStreamChunk{
				ID:      requestID,
				Object:  "chat.completion.chunk",
				Created: createdAt,
				Model:   model,
				Choices: []proxyChatStreamDelta{
					{
						Index: 0,
						Delta: proxyChatMessageDelta{
							ToolCalls: []proxyToolCall{tc},
						},
					},
				},
			})
		case llm.StreamEventUsage:
			usage = event.Usage
		}
		return nil
	})
	if streamErr != nil {
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
		return
	}

	if finalResp != nil {
		if len(finalResp.ToolCalls) > 0 {
			finishReason = "tool_calls"
		} else if strings.TrimSpace(finalResp.StopReason) != "" {
			finishReason = strings.TrimSpace(finalResp.StopReason)
		}
		if usage.InputTokens == 0 && usage.OutputTokens == 0 {
			usage = finalResp.Usage
		}
	}

	if finishReason == "" {
		finishReason = "stop"
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 {
		usage = llm.TokenUsage{}
	}

	_ = writeChunk(proxyChatStreamChunk{
		ID:      requestID,
		Object:  "chat.completion.chunk",
		Created: createdAt,
		Model:   model,
		Choices: []proxyChatStreamDelta{
			{
				Index:        0,
				Delta:        proxyChatMessageDelta{},
				FinishReason: &finishReason,
			},
		},
	})
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}

func proxyChatResponseFromLLM(resp *llm.ChatResponse, providerType config.ProviderType, model string) proxyChatResponse {
	if resp == nil {
		return proxyChatResponse{
			ID:      "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []proxyChoice{
				{
					Index: 0,
					Message: proxyMessage{
						Role:    "assistant",
						Content: "",
					},
					FinishReason: "stop",
				},
			},
			Usage: proxyUsage{},
			XA2gent: &proxyRoutingDetail{
				Provider: string(providerType),
			},
		}
	}

	message := proxyMessage{
		Role:    "assistant",
		Content: resp.Content,
	}
	if len(resp.ToolCalls) > 0 {
		toolCalls := make([]proxyToolCall, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			item := proxyToolCall{
				ID:   tc.ID,
				Type: "function",
			}
			item.Function.Name = tc.Name
			item.Function.Arguments = tc.Input
			toolCalls = append(toolCalls, item)
		}
		message.ToolCalls = toolCalls
	}

	finishReason := strings.TrimSpace(resp.StopReason)
	if finishReason == "" {
		if len(resp.ToolCalls) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	}

	return proxyChatResponse{
		ID:      "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []proxyChoice{
			{
				Index:        0,
				Message:      message,
				FinishReason: finishReason,
			},
		},
		Usage: proxyUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
		XA2gent: &proxyRoutingDetail{
			Provider: string(providerType),
		},
	}
}

func buildProxyLLMRequest(req *proxyChatRequest, resolvedModel string) (*llm.ChatRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	var systemParts []string
	messages := make([]llm.Message, 0, len(req.Messages))
	for _, msg := range req.Messages {
		role := strings.TrimSpace(strings.ToLower(msg.Role))
		if role == "" {
			continue
		}
		text, images := flattenProxyMessageContent(msg.Content)
		switch role {
		case "system":
			if text != "" {
				systemParts = append(systemParts, text)
			}
		case "assistant":
			converted := llm.Message{
				Role:    "assistant",
				Content: text,
				Images:  images,
			}
			if len(msg.ToolCalls) > 0 {
				converted.ToolCalls = make([]llm.ToolCall, 0, len(msg.ToolCalls))
				for _, tc := range msg.ToolCalls {
					converted.ToolCalls = append(converted.ToolCalls, llm.ToolCall{
						ID:    strings.TrimSpace(tc.ID),
						Name:  strings.TrimSpace(tc.Function.Name),
						Input: strings.TrimSpace(tc.Function.Arguments),
					})
				}
			}
			messages = append(messages, converted)
		case "tool":
			toolCallID := strings.TrimSpace(msg.ToolCallID)
			if toolCallID == "" {
				toolCallID = strings.TrimSpace(msg.Name)
			}
			messages = append(messages, llm.Message{
				Role: "tool",
				ToolResults: []llm.ToolResult{
					{
						ToolCallID: toolCallID,
						Content:    text,
						Name:       strings.TrimSpace(msg.Name),
					},
				},
			})
		default:
			messages = append(messages, llm.Message{
				Role:    role,
				Content: text,
				Images:  images,
			})
		}
	}

	tools := make([]llm.ToolDefinition, 0, len(req.Tools))
	for _, tool := range req.Tools {
		if strings.TrimSpace(strings.ToLower(tool.Type)) != "function" {
			continue
		}
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			continue
		}
		tools = append(tools, llm.ToolDefinition{
			Name:        name,
			Description: strings.TrimSpace(tool.Function.Description),
			InputSchema: tool.Function.Parameters,
		})
	}

	temperature := 0.0
	if req.Temperature != nil {
		temperature = *req.Temperature
	}
	maxTokens := 0
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	return &llm.ChatRequest{
		Model:        strings.TrimSpace(resolvedModel),
		Messages:     messages,
		Tools:        tools,
		Temperature:  temperature,
		MaxTokens:    maxTokens,
		SystemPrompt: strings.Join(systemParts, "\n\n"),
	}, nil
}

func flattenProxyMessageContent(raw any) (string, []llm.Image) {
	if raw == nil {
		return "", nil
	}
	switch value := raw.(type) {
	case string:
		return value, nil
	case []interface{}:
		texts := make([]string, 0, len(value))
		images := make([]llm.Image, 0, len(value))
		for _, item := range value {
			part, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			partType := strings.TrimSpace(strings.ToLower(proxyAnyToString(part["type"])))
			switch partType {
			case "text", "input_text":
				text := strings.TrimSpace(proxyAnyToString(part["text"]))
				if text != "" {
					texts = append(texts, text)
				}
			case "image_url", "input_image":
				imageURL := ""
				if rawURL, exists := part["image_url"]; exists {
					switch typed := rawURL.(type) {
					case string:
						imageURL = strings.TrimSpace(typed)
					case map[string]interface{}:
						imageURL = strings.TrimSpace(proxyAnyToString(typed["url"]))
					}
				}
				if imageURL == "" {
					imageURL = strings.TrimSpace(proxyAnyToString(part["url"]))
				}
				if imageURL == "" {
					continue
				}
				image := llm.Image{URL: imageURL}
				if strings.HasPrefix(strings.ToLower(imageURL), "data:") {
					if mediaType, dataBase64 := parseDataURL(imageURL); dataBase64 != "" {
						image.URL = ""
						image.MediaType = mediaType
						image.DataBase64 = dataBase64
					}
				}
				images = append(images, image)
			}
		}
		return strings.Join(texts, "\n"), images
	default:
		return fmt.Sprintf("%v", raw), nil
	}
}

func parseDataURL(raw string) (string, string) {
	const prefix = "data:"
	value := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(value), prefix) {
		return "", ""
	}
	comma := strings.Index(value, ",")
	if comma <= len(prefix) {
		return "", ""
	}
	meta := value[len(prefix):comma]
	dataPart := strings.TrimSpace(value[comma+1:])
	if dataPart == "" {
		return "", ""
	}
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return "", ""
	}
	mediaType := strings.TrimSpace(strings.Split(meta, ";")[0])
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	// Validate base64 data before forwarding.
	if _, err := base64.StdEncoding.DecodeString(dataPart); err != nil {
		return "", ""
	}
	return mediaType, dataPart
}

func proxyAnyToString(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	case json.Number:
		return value.String()
	default:
		return fmt.Sprintf("%v", raw)
	}
}

func (s *Server) resolveProxyProviderAndModel(pathProviderRef, requestProviderRef, requestModel string) (config.ProviderType, string, error) {
	providerRef := config.NormalizeProviderRef(pathProviderRef)
	if providerRef == "" {
		providerRef = config.NormalizeProviderRef(requestProviderRef)
	}

	model := strings.TrimSpace(requestModel)
	if providerRef == "" && model != "" {
		if idx := strings.Index(model, "/"); idx > 0 {
			candidateProvider := config.NormalizeProviderRef(model[:idx])
			if candidateProvider != "" {
				if config.GetProviderDefinition(config.ProviderType(candidateProvider)) != nil || s.providerRefExists(candidateProvider) {
					providerRef = candidateProvider
					model = strings.TrimSpace(model[idx+1:])
				}
			}
		}
	}

	if providerRef == "" {
		providerRef = config.NormalizeProviderRef(s.config.ActiveProvider)
	}
	if providerRef == "" {
		return "", "", fmt.Errorf("no active provider is configured")
	}
	if err := s.validateProviderRefForExecution(providerRef); err != nil {
		return "", "", fmt.Errorf("provider is not configured: %s", providerRef)
	}

	providerType := config.ProviderType(providerRef)
	if model == "" {
		model = strings.TrimSpace(s.resolveModelForProvider(providerType))
	}
	if model == "" && providerType != config.ProviderFallback && providerType != config.ProviderAutoRouter && !config.IsFallbackAggregateRef(providerRef) {
		return "", "", fmt.Errorf("no model configured for provider: %s", providerRef)
	}
	return providerType, model, nil
}
