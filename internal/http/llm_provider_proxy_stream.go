package http

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/google/uuid"
)

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
			for i, tc := range resp.ToolCalls {
				item := proxyToolCallFromLLM(tc)
				item.Index = intPtr(i)
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

	streamedContent := false
	streamedToolArguments := map[int]bool{}
	finalResp, streamErr := streamingClient.ChatStream(r.Context(), chatReq, func(event llm.StreamEvent) error {
		switch event.Type {
		case llm.StreamEventContentDelta:
			if strings.TrimSpace(event.ContentDelta) == "" {
				return nil
			}
			err := writeChunk(proxyChatStreamChunk{
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
			if err == nil {
				streamedContent = true
			}
			return err
		case llm.StreamEventToolCallDelta:
			tc := proxyToolCallFromStreamEvent(event)
			if strings.TrimSpace(event.ToolInputDelta) != "" {
				streamedToolArguments[event.ToolCallIndex] = true
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

	if finalResp != nil && strings.TrimSpace(finalResp.Content) != "" && !streamedContent {
		_ = writeChunk(proxyChatStreamChunk{
			ID:      requestID,
			Object:  "chat.completion.chunk",
			Created: createdAt,
			Model:   model,
			Choices: []proxyChatStreamDelta{
				{
					Index: 0,
					Delta: proxyChatMessageDelta{
						Content: finalResp.Content,
					},
				},
			},
		})
	}
	if finalResp != nil && len(finalResp.ToolCalls) > 0 {
		toolCalls := make([]proxyToolCall, 0, len(finalResp.ToolCalls))
		for i, tc := range finalResp.ToolCalls {
			item := proxyToolCallFromLLM(tc)
			item.Index = intPtr(i)
			if streamedToolArguments[i] {
				item.Function.Arguments = ""
			}
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
