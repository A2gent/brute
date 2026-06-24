package a2atunnel

import (
	"context"
	"encoding/json"
	"testing"
)

func TestHandleInternalEventReturnsRawPayloadWhenProvided(t *testing.T) {
	t.Parallel()

	handler := &InboundHandler{
		internalEventHandler: func(_ context.Context, payload json.RawMessage) (*InternalEventResult, error) {
			return &InternalEventResult{Payload: payload}, nil
		},
	}
	input := json.RawMessage(`{"http":{"status_code":200}}`)

	got, err := handler.handleInternalEvent(context.Background(), "brute_http_request", input)
	if err != nil {
		t.Fatalf("handleInternalEvent: %v", err)
	}
	if string(got) != string(input) {
		t.Fatalf("expected raw payload passthrough, got %s", string(got))
	}
}

func TestHandleInternalEventWrapsLegacyConversationIDResponse(t *testing.T) {
	t.Parallel()

	handler := &InboundHandler{
		internalEventHandler: func(_ context.Context, payload json.RawMessage) (*InternalEventResult, error) {
			return &InternalEventResult{ConversationID: "sess-123"}, nil
		},
	}

	got, err := handler.handleInternalEvent(context.Background(), "leonardo_webhook", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handleInternalEvent: %v", err)
	}
	var out OutboundPayload
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("decode outbound payload: %v", err)
	}
	if out.ConversationID != "sess-123" {
		t.Fatalf("expected conversation id sess-123, got %q", out.ConversationID)
	}
	if out.Result == "" {
		t.Fatal("expected legacy wrapped result text")
	}
}
