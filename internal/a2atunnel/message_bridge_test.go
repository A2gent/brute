package a2atunnel

import "testing"

func TestBuildAndExtractA2AContent(t *testing.T) {
	t.Parallel()

	parts := BuildA2AContent("hello", []A2AImage{{URL: "https://example.com/x.png"}})
	if len(parts) != 2 {
		t.Fatalf("expected two parts, got %d", len(parts))
	}
	if parts[0].Type != A2AContentTypeText {
		t.Fatalf("expected first part text, got %q", parts[0].Type)
	}
	text, images := LegacyFromA2AContent(parts)
	if text != "hello" {
		t.Fatalf("expected text hello, got %q", text)
	}
	if len(images) != 1 || images[0].URL != "https://example.com/x.png" {
		t.Fatalf("expected one image, got %#v", images)
	}
}

func TestLegacyFromA2AContentImageOnly(t *testing.T) {
	t.Parallel()

	text, images := LegacyFromA2AContent([]A2AContentPart{{Type: A2AContentTypeImageBase64, Data: "Zm9v", MediaType: "image/png"}})
	if text != "" {
		t.Fatalf("expected empty text, got %q", text)
	}
	if len(images) != 1 || images[0].DataBase64 != "Zm9v" {
		t.Fatalf("expected one base64 image, got %#v", images)
	}
}
