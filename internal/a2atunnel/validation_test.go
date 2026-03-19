package a2atunnel

import (
	"encoding/base64"
	"fmt"
	"testing"
)

func TestValidateA2AContentAcceptsImageURLAndBase64(t *testing.T) {
	t.Parallel()

	err := ValidateA2AContent([]A2AContentPart{
		{Type: A2AContentTypeText, Text: "check this"},
		{Type: A2AContentTypeImageURL, URL: "https://example.com/a.png"},
		{Type: A2AContentTypeImageBase64, MediaType: "image/png", Data: "Zm9v"},
	})
	if err != nil {
		t.Fatalf("expected content to be valid, got %v", err)
	}
}

func TestValidateA2AContentRejectsTooManyImages(t *testing.T) {
	t.Parallel()

	content := make([]A2AContentPart, 0, MaxA2AImagesPerMessage+1)
	for i := 0; i < MaxA2AImagesPerMessage+1; i++ {
		content = append(content, A2AContentPart{
			Type: A2AContentTypeImageURL,
			URL:  fmt.Sprintf("https://example.com/%d.png", i),
		})
	}
	if err := ValidateA2AContent(content); err == nil {
		t.Fatal("expected too many images error")
	}
}

func TestValidateA2AImagesRejectsOversizedBase64(t *testing.T) {
	t.Parallel()

	oversized := base64.StdEncoding.EncodeToString(make([]byte, MaxA2AImageDecodedBytes+1))
	err := ValidateA2AImages([]A2AImage{{
		MediaType:  "image/png",
		DataBase64: oversized,
	}})
	if err == nil {
		t.Fatal("expected oversized base64 error")
	}
}

func TestValidateA2AImagesRejectsUnsupportedMediaType(t *testing.T) {
	t.Parallel()

	err := ValidateA2AImages([]A2AImage{{
		MediaType:  "image/svg+xml",
		DataBase64: "Zm9v",
	}})
	if err == nil {
		t.Fatal("expected unsupported media type error")
	}
}
