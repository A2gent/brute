package agent

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestFitRequestImages(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 8200, 300))); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("A", base64.StdEncoding.EncodedLen(llm.ProviderImageMaxBytes)+4)
	messages := []llm.Message{{Role: "user", Images: []llm.Image{
		{MediaType: "image/png", DataBase64: base64.StdEncoding.EncodeToString(buf.Bytes())},
		{MediaType: "image/png", DataBase64: "c21hbGw="},
		{MediaType: "image/png", DataBase64: huge},
	}}}
	fitRequestImages(messages)
	images := messages[0].Images
	if len(images) != 2 || images[1].DataBase64 != "c21hbGw=" || !strings.Contains(messages[0].Content, "image omitted") {
		t.Fatalf("unexpected images: %d, content %q", len(images), messages[0].Content)
	}
	raw, _ := base64.StdEncoding.DecodeString(images[0].DataBase64)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width > llm.ProviderImageMaxSide {
		t.Fatalf("large image not fitted: %v %d", err, cfg.Width)
	}
}
