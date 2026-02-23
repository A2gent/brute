package http

import (
	"encoding/json"
	"testing"

	"github.com/A2gent/brute/internal/a2atunnel"
)

func TestDecodeA2AOutboundResultStructuredWithImages(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(a2atunnel.OutboundPayload{
		Result: "done",
		Images: []a2atunnel.A2AImage{
			{
				Name:       "receipt",
				MediaType:  "image/png",
				DataBase64: "Zm9v",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	text, images := decodeA2AOutboundResult(payload)
	if text != "done" {
		t.Fatalf("expected result text 'done', got %q", text)
	}
	if len(images) != 1 {
		t.Fatalf("expected one image, got %d", len(images))
	}
	if images[0].MediaType != "image/png" {
		t.Fatalf("expected image/png media type, got %q", images[0].MediaType)
	}
}

func TestDecodeA2AOutboundResultFallbackText(t *testing.T) {
	t.Parallel()

	text, images := decodeA2AOutboundResult([]byte(`"hello"`))
	if text != "hello" {
		t.Fatalf("expected plain text result, got %q", text)
	}
	if len(images) != 0 {
		t.Fatalf("expected no images, got %d", len(images))
	}
}

func TestDecodeA2AOutboundResultFromContentParts(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(a2atunnel.OutboundPayload{
		A2AVersion: a2atunnel.A2ABridgeVersion,
		Content: []a2atunnel.A2AContentPart{
			{Type: a2atunnel.A2AContentTypeText, Text: "hello from content"},
			{Type: a2atunnel.A2AContentTypeImageURL, URL: "https://example.com/image.png"},
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	text, images := decodeA2AOutboundResult(payload)
	if text != "hello from content" {
		t.Fatalf("expected derived content text, got %q", text)
	}
	if len(images) != 1 || images[0].URL != "https://example.com/image.png" {
		t.Fatalf("expected derived image from content, got %#v", images)
	}
}
