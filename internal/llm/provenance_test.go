package llm

import (
	"context"
	"testing"
)

type provenanceTestClient struct {
	response *ChatResponse
	event    StreamEvent
}

func (c *provenanceTestClient) Chat(context.Context, *ChatRequest) (*ChatResponse, error) {
	return c.response, nil
}

type provenanceTestStreamClient struct{ *provenanceTestClient }

func (c *provenanceTestStreamClient) ChatStream(_ context.Context, _ *ChatRequest, onEvent func(StreamEvent) error) (*ChatResponse, error) {
	if onEvent != nil {
		if err := onEvent(c.event); err != nil {
			return nil, err
		}
	}
	return c.response, nil
}
func TestProvenanceNestedWrappersAndCopies(t *testing.T) {
	raw := &ChatResponse{Content: "hello"}
	client := &provenanceTestStreamClient{&provenanceTestClient{response: raw, event: StreamEvent{Type: StreamEventContentDelta, ContentDelta: "hello"}}}
	wrapped := WithProvenance(WithProvenance(client, "anthropic:work", "model-a"), "fallback_chain:work", "aggregate")
	var first *Provenance
	for i := 0; i < 2; i++ {
		resp, err := wrapped.(StreamingClient).ChatStream(context.Background(), &ChatRequest{}, func(ev StreamEvent) error {
			if ev.Provenance == nil || ev.Provenance.Provider != "anthropic:work" {
				t.Fatalf("event = %+v", ev)
			}
			if i == 0 {
				first = ev.Provenance
				first.Provider = "changed by consumer"
			}
			return nil
		})
		if err != nil || resp.Provenance == nil || resp.Provenance.Provider != "anthropic:work" {
			t.Fatalf("response=%+v err=%v", resp, err)
		}
		resp.Provenance.Model = "changed by consumer"
	}
	if raw.Provenance != nil {
		t.Fatal("mutated original response")
	}
}
func TestProvenancePreservesNonStreamingAndRejectsAggregates(t *testing.T) {
	client := &provenanceTestClient{response: &ChatResponse{Content: "hello"}}
	wrapped := WithProvenance(client, "openai:personal", "model-b")
	if _, ok := wrapped.(StreamingClient); ok {
		t.Fatal("invented streaming support")
	}
	resp, err := wrapped.Chat(context.Background(), &ChatRequest{})
	if err != nil || resp.Provenance == nil || resp.Provenance.Provider != "openai:personal" {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
	for _, provider := range []string{"", "automatic_router", "fallback_chain", "fallback_chain:work"} {
		if NewProvenance(provider, "aggregate") != nil {
			t.Errorf("accepted aggregate %q", provider)
		}
	}
}

func TestProvenanceUsesTransportModelWithoutLosingNamedInstance(t *testing.T) {
	client := &provenanceTestStreamClient{&provenanceTestClient{
		response: &ChatResponse{Content: "hello", Model: "resolved-model"},
		event:    StreamEvent{Type: StreamEventContentDelta, ContentDelta: "hello", Model: "resolved-model"},
	}}
	wrapped := WithProvenance(client, "openai:work", "alias")
	response, err := wrapped.(StreamingClient).ChatStream(context.Background(), &ChatRequest{}, func(ev StreamEvent) error {
		if ev.Provenance == nil || ev.Provenance.Model != "resolved-model" || ev.Provenance.Provider != "openai:work" {
			t.Fatalf("event=%+v", ev)
		}
		return nil
	})
	if err != nil || response.Provenance.Model != "resolved-model" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
