package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

func TestReadImage(t *testing.T) {
	data := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZ1kAAAAASUVORK5CYII="
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, oversized := range []bool{false, true} {
		name := "screenshot.png"
		if oversized {
			name = "oversized.png"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if oversized {
				if err := os.Truncate(path, maxReadImageBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			params, _ := json.Marshal(map[string]string{"path": path})
			result, err := NewReadTool(dir).Execute(context.Background(), params)
			if err != nil {
				t.Fatal(err)
			}
			if oversized {
				if result.Success || !strings.Contains(result.Error, "image exceeds") {
					t.Fatalf("oversized image was admitted: %+v", result)
				}
				return
			}
			inline, _ := result.Metadata["image_inline"].(map[string]interface{})
			if !result.Success || inline["media_type"] != "image/png" || inline["data_base64"] != data || strings.Contains(result.Output, data) {
				t.Fatalf("image read did not use the image channel: %+v", result)
			}
		})
	}
}

func TestReadImageDownscalesLargePNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8500, 1000))
	for x := 0; x < 8500; x++ {
		img.Set(x, x%1000, color.Black)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "wide.png")
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(map[string]string{"path": path})
	result, err := NewReadTool(dir).Execute(context.Background(), params)
	if err != nil || !result.Success {
		t.Fatalf("read failed: %v %+v", err, result)
	}
	inline, _ := result.Metadata["image_inline"].(map[string]interface{})
	raw, _ := base64.StdEncoding.DecodeString(inline["data_base64"].(string))
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || inline["media_type"] != "image/"+format || cfg.Width > llm.ProviderImageMaxSide || len(raw) > llm.ProviderImageMaxBytes {
		t.Fatalf("image not fitted: %v %v %dx%d %d bytes", err, inline["media_type"], cfg.Width, cfg.Height, len(raw))
	}
}
