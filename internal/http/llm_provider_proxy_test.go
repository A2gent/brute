package http

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
)

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
