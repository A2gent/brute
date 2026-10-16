package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
