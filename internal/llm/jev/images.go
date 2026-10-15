package jev

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/A2gent/brute/internal/llm"
)

// Jev accepts only PNG/JPEG images whose sides are 256-1568 px and whose area is at
// least 280x280 (https://docs.typesafe.ai/go/images); it rejects instead of resizing.
const (
	imageMinSide = 280
	// Routing needs the gist, not detail: a small copy keeps image tokens low.
	imageMaxSide   = 768
	routerMaxImage = 4
	imagesHeader   = "TypeSafe-Danger-Zone-Images"
)

// Asset is one image in a System One request, referenced by its map key.
type Asset struct {
	Type       string `json:"type"`
	Format     string `json:"format"`
	DataBase64 string `json:"data_base64"`
}

// prepareImage re-encodes an attachment as a baseline JPEG fitted to Jev's size limits:
// large images are downscaled, small or very thin ones are padded onto a white canvas.
func prepareImage(img llm.Image) (Asset, error) {
	if img.DataBase64 == "" {
		return Asset{}, fmt.Errorf("image has no inline data")
	}
	raw, err := base64.StdEncoding.DecodeString(img.DataBase64)
	if err != nil {
		return Asset{}, fmt.Errorf("decode base64: %w", err)
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Asset{}, fmt.Errorf("decode image: %w", err)
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	if w <= 0 || h <= 0 {
		return Asset{}, fmt.Errorf("empty image")
	}
	if longest := max(w, h); longest > imageMaxSide {
		w, h = max(1, w*imageMaxSide/longest), max(1, h*imageMaxSide/longest)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, max(w, imageMinSide), max(h, imageMinSide)))
	stddraw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, stddraw.Src)
	offset := image.Pt((canvas.Bounds().Dx()-w)/2, (canvas.Bounds().Dy()-h)/2)
	draw.ApproxBiLinear.Scale(canvas, image.Rectangle{Min: offset, Max: offset.Add(image.Pt(w, h))}, src, src.Bounds(), draw.Over, nil)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, canvas, &jpeg.Options{Quality: 85}); err != nil {
		return Asset{}, fmt.Errorf("encode jpeg: %w", err)
	}
	return Asset{Type: "image", Format: "jpg", DataBase64: base64.StdEncoding.EncodeToString(out.Bytes())}, nil
}
