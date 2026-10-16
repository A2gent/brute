package llm

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Provider-safe image bounds. Anthropic rejects images over 5 MB base64 or
// 8000 px per side (2000 px when a request carries many images), and one
// rejected image fails every later request of the session, so stay well below.
const (
	ProviderImageMaxSide  = 2000
	ProviderImageMaxBytes = 3_500_000
)

// configPrefixBytes is enough for DecodeConfig on PNG/GIF/WebP headers and
// typical JPEG EXIF blocks, so in-limit images are not fully decoded per request.
const configPrefixBytes = 256 * 1024

// FitImage returns an inline image that satisfies provider limits. URL-only or
// already fitting images are returned unchanged.
func FitImage(img Image) (Image, error) {
	if img.DataBase64 == "" {
		return img, nil
	}
	data := strings.TrimSpace(img.DataBase64)
	if base64.StdEncoding.DecodedLen(len(data)) <= ProviderImageMaxBytes {
		prefix := data[:min(len(data), base64.StdEncoding.EncodedLen(configPrefixBytes))]
		if head, err := base64.StdEncoding.DecodeString(prefix); err == nil {
			if cfg, _, err := image.DecodeConfig(bytes.NewReader(head)); err == nil && max(cfg.Width, cfg.Height) <= ProviderImageMaxSide {
				return img, nil
			}
		}
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return img, fmt.Errorf("decode base64: %w", err)
	}
	mediaType, fitted, err := FitImageBytes(img.MediaType, raw)
	if err != nil {
		return img, err
	}
	img.MediaType = mediaType
	img.DataBase64 = base64.StdEncoding.EncodeToString(fitted)
	return img, nil
}

// FitImageBytes downscales and re-encodes raw image bytes when they exceed
// ProviderImageMaxSide or ProviderImageMaxBytes. PNG stays PNG when it fits
// (sharper screenshots); otherwise the result is JPEG.
func FitImageBytes(mediaType string, raw []byte) (string, []byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return mediaType, raw, fmt.Errorf("decode image: %w", err)
	}
	if len(raw) <= ProviderImageMaxBytes && max(cfg.Width, cfg.Height) <= ProviderImageMaxSide {
		return mediaType, raw, nil
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return mediaType, raw, fmt.Errorf("decode image: %w", err)
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	if w <= 0 || h <= 0 {
		return mediaType, raw, fmt.Errorf("empty image")
	}
	if longest := max(w, h); longest > ProviderImageMaxSide {
		w, h = max(1, w*ProviderImageMaxSide/longest), max(1, h*ProviderImageMaxSide/longest)
	}
	// White background flattens transparency for the JPEG fallback.
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	stddraw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, stddraw.Src)
	draw.ApproxBiLinear.Scale(canvas, canvas.Bounds(), src, src.Bounds(), draw.Over, nil)

	var out bytes.Buffer
	if format == "png" {
		if err := png.Encode(&out, canvas); err == nil && out.Len() <= ProviderImageMaxBytes {
			return "image/png", out.Bytes(), nil
		}
	}
	for _, quality := range []int{85, 70, 50} {
		out.Reset()
		if err := jpeg.Encode(&out, canvas, &jpeg.Options{Quality: quality}); err != nil {
			return mediaType, raw, fmt.Errorf("encode jpeg: %w", err)
		}
		if out.Len() <= ProviderImageMaxBytes {
			break
		}
	}
	return "image/jpeg", out.Bytes(), nil
}
