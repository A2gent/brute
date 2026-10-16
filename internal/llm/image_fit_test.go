package llm

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"testing"
)

func encodePNG(t *testing.T, w, h int, noise bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if noise {
				img.Set(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255})
			} else {
				img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func assertWithinLimits(t *testing.T, mediaType string, raw []byte) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("fitted image does not decode: %v", err)
	}
	if mediaType != "image/"+format {
		t.Fatalf("media type %q does not match format %q", mediaType, format)
	}
	if len(raw) > ProviderImageMaxBytes || max(cfg.Width, cfg.Height) > ProviderImageMaxSide {
		t.Fatalf("fitted image too large: %d bytes, %dx%d", len(raw), cfg.Width, cfg.Height)
	}
}

func TestFitImageBytes(t *testing.T) {
	t.Run("too many pixels", func(t *testing.T) {
		raw := encodePNG(t, 9000, 1200, false)
		mediaType, fitted, err := FitImageBytes("image/png", raw)
		if err != nil {
			t.Fatal(err)
		}
		assertWithinLimits(t, mediaType, fitted)
	})
	t.Run("too many bytes", func(t *testing.T) {
		raw := encodePNG(t, 1500, 1500, true)
		if len(raw) <= ProviderImageMaxBytes {
			t.Fatalf("fixture too small: %d", len(raw))
		}
		mediaType, fitted, err := FitImageBytes("image/png", raw)
		if err != nil {
			t.Fatal(err)
		}
		assertWithinLimits(t, mediaType, fitted)
	})
	t.Run("small unchanged", func(t *testing.T) {
		raw := encodePNG(t, 64, 64, false)
		mediaType, fitted, err := FitImageBytes("image/png", raw)
		if err != nil || mediaType != "image/png" || !bytes.Equal(raw, fitted) {
			t.Fatalf("small image changed: %v %s", err, mediaType)
		}
	})
	t.Run("undecodable", func(t *testing.T) {
		if _, _, err := FitImageBytes("image/png", []byte("not an image")); err == nil {
			t.Fatal("expected decode error")
		}
	})
}

func TestFitImage(t *testing.T) {
	small := Image{MediaType: "image/png", DataBase64: base64.StdEncoding.EncodeToString(encodePNG(t, 64, 64, false))}
	if got, err := FitImage(small); err != nil || got != small {
		t.Fatalf("small image changed: %v", err)
	}
	large := Image{Name: "wide", MediaType: "image/png", DataBase64: base64.StdEncoding.EncodeToString(encodePNG(t, 9000, 1200, false))}
	got, err := FitImage(large)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(got.DataBase64)
	assertWithinLimits(t, got.MediaType, raw)
	if got.Name != "wide" {
		t.Fatalf("name lost: %+v", got.Name)
	}
}
