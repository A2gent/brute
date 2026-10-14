package http

import (
	"context"
	"encoding/json"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/lmstudio"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyProvenanceRoundTrip(t *testing.T) {
	p := llm.NewProvenance("anthropic:work", "actual-model")
	producer := proxyTestStreamingClient{
		events: []llm.StreamEvent{{Type: llm.StreamEventContentDelta, ContentDelta: "answer", Provenance: p}},
		final:  &llm.ChatResponse{Content: "answer", Provenance: p},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req proxyChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Stream {
			(&Server{}).handleLLMProxyChatCompletionsStream(w, r, producer, &llm.ChatRequest{}, "aggregate-model")
		} else {
			_ = json.NewEncoder(w).Encode(proxyChatResponseFromLLM(producer.final, config.ProviderFallback, "aggregate-model"))
		}
	}))
	defer server.Close()
	client := llm.WithProvenance(lmstudio.NewClient("", "", server.URL), "fallback_chain:work", "")
	request := &llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "hi"}}}
	response, err := client.Chat(context.Background(), request)
	if err != nil || response.Provenance == nil || *response.Provenance != *p {
		t.Fatalf("nonstream=%+v err=%v", response, err)
	}
	seen := false
	response, err = client.(llm.StreamingClient).ChatStream(context.Background(), request, func(ev llm.StreamEvent) error {
		if ev.Type == llm.StreamEventContentDelta {
			seen = true
			if ev.Provenance == nil || *ev.Provenance != *p {
				t.Errorf("stream provenance=%+v", ev.Provenance)
			}
		}
		return nil
	})
	if err != nil || !seen || response.Provenance == nil || *response.Provenance != *p {
		t.Fatalf("stream=%+v err=%v seen=%v", response, err, seen)
	}
}
