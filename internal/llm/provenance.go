package llm

import (
	"context"
	"strings"
)

// Provenance identifies the concrete producer, not the mutable session selection.
type Provenance struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func NewProvenance(provider, model string) *Provenance {
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	base := strings.SplitN(provider, ":", 2)[0]
	if provider == "" || base == "automatic_router" || base == "fallback_chain" {
		return nil
	}
	return &Provenance{Provider: provider, Model: model}
}

// WithProvenance stamps the request boundary; inner routing wrappers take priority.
// Values are copied so later requests cannot mutate already emitted provenance.
func WithProvenance(client Client, provider, model string) Client {
	wrapped := &provenanceClient{Client: client, provenance: NewProvenance(provider, model)}
	if _, ok := client.(StreamingClient); ok {
		return &streamProvenanceClient{wrapped}
	}
	return wrapped
}

type provenanceClient struct {
	Client
	provenance *Provenance
}

func (c *provenanceClient) stamp(p *Provenance, model string) *Provenance {
	if p != nil {
		return NewProvenance(p.Provider, p.Model)
	}
	if c.provenance == nil {
		return nil
	}
	if strings.TrimSpace(model) == "" {
		model = c.provenance.Model
	}
	return NewProvenance(c.provenance.Provider, model)
}
func (c *provenanceClient) response(response *ChatResponse) *ChatResponse {
	if response == nil {
		return nil
	}
	copy := *response
	copy.Provenance = c.stamp(response.Provenance, response.Model)
	return &copy
}
func (c *provenanceClient) Chat(ctx context.Context, request *ChatRequest) (*ChatResponse, error) {
	response, err := c.Client.Chat(ctx, request)
	return c.response(response), err
}

type streamProvenanceClient struct{ *provenanceClient }

func (c *streamProvenanceClient) ChatStream(ctx context.Context, request *ChatRequest, onEvent func(StreamEvent) error) (*ChatResponse, error) {
	client, ok := c.Client.(StreamingClient)
	if !ok {
		return c.Chat(ctx, request)
	}
	response, err := client.ChatStream(ctx, request, func(ev StreamEvent) error {
		model := ev.Model
		if ev.Type == StreamEventProviderTrace {
			model = ""
		}
		ev.Provenance = c.stamp(ev.Provenance, model)
		if onEvent != nil {
			return onEvent(ev)
		}
		return nil
	})
	return c.response(response), err
}
