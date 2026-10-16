package agent

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestSessionImagesToLLMLoadsFileLazily(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	original := session.ImageAttachment{MediaType: "image/png", DataBase64: base64.StdEncoding.EncodeToString(buf.Bytes())}
	saved, err := original.Save(root, "session")
	if err != nil {
		t.Fatal(err)
	}
	images := sessionImagesToLLM([]session.ImageAttachment{saved})
	if len(images) != 1 || images[0].DataBase64 != original.DataBase64 {
		t.Fatal("request did not load file")
	}
	if saved.DataBase64 != "" {
		t.Fatal("hydration modified persistent attachment")
	}
	if err := os.Remove(filepath.Join(root, saved.Path)); err != nil {
		t.Fatal(err)
	}
	if len(sessionImagesToLLM([]session.ImageAttachment{saved})) != 0 {
		t.Fatal("sent broken image")
	}
}
