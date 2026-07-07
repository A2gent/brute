package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
)

type proxyTestStreamingClient struct {
	events []llm.StreamEvent
	final  *llm.ChatResponse
}

func (c proxyTestStreamingClient) Chat(ctx context.Context, request *llm.ChatRequest) (*llm.ChatResponse, error) {
	return c.final, nil
}

func (c proxyTestStreamingClient) ChatStream(ctx context.Context, request *llm.ChatRequest, onEvent func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	for _, event := range c.events {
		if onEvent != nil {
			if err := onEvent(event); err != nil {
				return nil, err
			}
		}
	}
	return c.final, nil
}

func TestProxyChatResponseFromLLMPreservesToolCallThoughtSignature(t *testing.T) {
	resp := &llm.ChatResponse{
		ToolCalls: []llm.ToolCall{
			{
				ID:               "call_fetch",
				Name:             "fetch_url",
				Input:            `{"url":"https://a2gent.net/"}`,
				ThoughtSignature: "gemini-signature",
			},
		},
	}

	proxied := proxyChatResponseFromLLM(resp, config.ProviderGoogle, "gemini-test")
	if len(proxied.Choices) != 1 || len(proxied.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected one proxied tool call, got %#v", proxied.Choices)
	}

	toolCall := proxied.Choices[0].Message.ToolCalls[0]
	if got := toolCall.Function.ThoughtSignature; got != "gemini-signature" {
		t.Fatalf("function thought_signature = %q", got)
	}
	if toolCall.ExtraContent == nil || toolCall.ExtraContent.Google.ThoughtSignature != "gemini-signature" {
		t.Fatalf("extra_content thought_signature not preserved: %#v", toolCall.ExtraContent)
	}

	encoded, err := json.Marshal(proxied)
	if err != nil {
		t.Fatalf("marshal proxied response: %v", err)
	}
	if !strings.Contains(string(encoded), "thought_signature") {
		t.Fatalf("expected serialized proxy response to include thought_signature: %s", string(encoded))
	}
}

func TestProxyToolCallFromStreamEventPreservesThoughtSignature(t *testing.T) {
	toolCall := proxyToolCallFromStreamEvent(llm.StreamEvent{
		Type:                     llm.StreamEventToolCallDelta,
		ToolCallIndex:            2,
		ToolCallID:               "call_fetch",
		ToolCallName:             "fetch_url",
		ToolInputDelta:           `{"url":"https://a2gent.net/"}`,
		ToolCallThoughtSignature: "stream-signature",
	})

	if toolCall.Index == nil || *toolCall.Index != 2 {
		t.Fatalf("index not preserved: %#v", toolCall.Index)
	}
	if got := toolCall.Function.ThoughtSignature; got != "stream-signature" {
		t.Fatalf("function thought_signature = %q", got)
	}
	if toolCall.ExtraContent == nil || toolCall.ExtraContent.Google.ThoughtSignature != "stream-signature" {
		t.Fatalf("extra_content thought_signature not preserved: %#v", toolCall.ExtraContent)
	}
}

func TestLLMProxyStreamFlushesFinalToolCalls(t *testing.T) {
	client := proxyTestStreamingClient{
		final: &llm.ChatResponse{
			ToolCalls: []llm.ToolCall{{
				ID:    "call_grep",
				Name:  "grep",
				Input: `{"pattern":"TODO"}`,
			}},
		},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/providers/openai_codex/chat/completions", nil)

	(&Server{}).handleLLMProxyChatCompletionsStream(rec, req, client, &llm.ChatRequest{}, "gpt-test")

	chunks := proxyStreamChunksFromBody(t, rec.Body.String())
	var found bool
	for _, chunk := range chunks {
		if len(chunk.Choices) == 0 || len(chunk.Choices[0].Delta.ToolCalls) == 0 {
			continue
		}
		toolCall := chunk.Choices[0].Delta.ToolCalls[0]
		if toolCall.ID == "call_grep" && toolCall.Function.Name == "grep" && toolCall.Function.Arguments == `{"pattern":"TODO"}` {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected final tool call chunk, got stream:\n%s", rec.Body.String())
	}
}

func TestLLMProxyStreamFinalToolCallDoesNotDuplicateStreamedArguments(t *testing.T) {
	client := proxyTestStreamingClient{
		events: []llm.StreamEvent{
			{Type: llm.StreamEventToolCallDelta, ToolCallIndex: 0, ToolCallID: "call_grep", ToolInputDelta: `{"pattern":`},
			{Type: llm.StreamEventToolCallDelta, ToolCallIndex: 0, ToolInputDelta: `"TODO"}`},
		},
		final: &llm.ChatResponse{
			ToolCalls: []llm.ToolCall{{
				ID:    "call_grep",
				Name:  "grep",
				Input: `{"pattern":"TODO"}`,
			}},
		},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/providers/openai_codex/chat/completions", nil)

	(&Server{}).handleLLMProxyChatCompletionsStream(rec, req, client, &llm.ChatRequest{}, "gpt-test")

	chunks := proxyStreamChunksFromBody(t, rec.Body.String())
	var foundFinalName bool
	for _, chunk := range chunks {
		if len(chunk.Choices) == 0 || len(chunk.Choices[0].Delta.ToolCalls) == 0 {
			continue
		}
		toolCall := chunk.Choices[0].Delta.ToolCalls[0]
		if toolCall.Function.Name == "grep" {
			foundFinalName = true
			if toolCall.Function.Arguments != "" {
				t.Fatalf("final name-bearing tool chunk duplicated arguments: %#v", toolCall)
			}
		}
	}
	if !foundFinalName {
		t.Fatalf("expected final tool-call name chunk, got stream:\n%s", rec.Body.String())
	}
}

func TestLLMProxyProviderCreditsForOpenRouter(t *testing.T) {
	creditsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/credits" {
			t.Fatalf("path = %q, want /v1/credits", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-openrouter-key" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"total_credits":1,"total_usage":1}}`))
	}))
	defer creditsServer.Close()

	server, cleanup := newRequestLoggingTestServer(t)
	defer cleanup()
	server.config.Providers[string(config.ProviderOpenRouter)] = config.Provider{
		BaseURL: creditsServer.URL + "/v1",
		APIKey:  "test-openrouter-key",
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/providers/openrouter/credits", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("credits status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"total_credits":1`) {
		t.Fatalf("unexpected credits body: %s", rec.Body.String())
	}
}

func proxyStreamChunksFromBody(t *testing.T, body string) []proxyChatStreamChunk {
	t.Helper()
	chunks := []proxyChatStreamChunk{}
	for _, block := range strings.Split(body, "\n\n") {
		line := strings.TrimSpace(block)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk proxyChatStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("failed to decode stream chunk %q: %v", payload, err)
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func TestBuildProxyLLMRequestPreservesToolCallThoughtSignature(t *testing.T) {
	idx := 0
	req := &proxyChatRequest{
		Messages: []proxyMessage{
			{
				Role: "assistant",
				ToolCalls: []proxyToolCall{
					{
						Index: &idx,
						ID:    "call_fetch",
						Type:  "function",
						Function: proxyToolFunction{
							Name:             "fetch_url",
							Arguments:        `{"url":"https://a2gent.net/"}`,
							ThoughtSignature: "gemini-signature",
						},
					},
				},
			},
		},
	}

	chatReq, err := buildProxyLLMRequest(req, "gemini-test")
	if err != nil {
		t.Fatalf("buildProxyLLMRequest returned error: %v", err)
	}
	if len(chatReq.Messages) != 1 || len(chatReq.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected one converted tool call, got %#v", chatReq.Messages)
	}
	if got := chatReq.Messages[0].ToolCalls[0].ThoughtSignature; got != "gemini-signature" {
		t.Fatalf("converted thought_signature = %q", got)
	}
}

func TestBuildProxyLLMRequestReadsThoughtSignatureFromExtraContent(t *testing.T) {
	req := &proxyChatRequest{
		Messages: []proxyMessage{
			{
				Role: "assistant",
				ToolCalls: []proxyToolCall{
					{
						ID:   "call_fetch",
						Type: "function",
						Function: proxyToolFunction{
							Name:      "fetch_url",
							Arguments: `{"url":"https://a2gent.net/"}`,
						},
						ExtraContent: &proxyExtraContent{Google: proxyGoogleExtra{ThoughtSignature: "extra-signature"}},
					},
				},
			},
		},
	}

	chatReq, err := buildProxyLLMRequest(req, "gemini-test")
	if err != nil {
		t.Fatalf("buildProxyLLMRequest returned error: %v", err)
	}
	if got := chatReq.Messages[0].ToolCalls[0].ThoughtSignature; got != "extra-signature" {
		t.Fatalf("converted extra_content thought_signature = %q", got)
	}
}
