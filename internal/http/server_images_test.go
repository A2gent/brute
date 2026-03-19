package http

import "testing"

func TestNormalizeIncomingImagesAcceptsValidImage(t *testing.T) {
	t.Parallel()

	images, err := normalizeIncomingImages([]MessageImagePayload{{
		MediaType:  "image/png",
		DataBase64: "Zm9v",
	}})
	if err != nil {
		t.Fatalf("expected valid image payload, got %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("expected one image, got %d", len(images))
	}
}

func TestNormalizeIncomingImagesRejectsUnsupportedMediaType(t *testing.T) {
	t.Parallel()

	_, err := normalizeIncomingImages([]MessageImagePayload{{
		MediaType:  "image/svg+xml",
		DataBase64: "Zm9v",
	}})
	if err == nil {
		t.Fatal("expected unsupported media type error")
	}
}

func TestNormalizeIncomingImagesRejectsInvalidURLScheme(t *testing.T) {
	t.Parallel()

	_, err := normalizeIncomingImages([]MessageImagePayload{{
		URL: "ftp://example.com/image.png",
	}})
	if err == nil {
		t.Fatal("expected invalid URL scheme error")
	}
}
