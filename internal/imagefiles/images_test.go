package imagefiles

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestAttachmentRoundtrip(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	original := Attachment{Name: "screen.png", MediaType: "image/png", DataBase64: base64.StdEncoding.EncodeToString(buf.Bytes())}
	saved, err := original.Save(root, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.DataBase64 != "" || saved.Path == "" || len(saved.SHA256) != 64 || saved.Width != 3 || saved.Height != 2 {
		t.Fatalf("bad reference: %+v", saved)
	}
	loaded, err := saved.Inline()
	if err != nil || loaded.DataBase64 != original.DataBase64 {
		t.Fatalf("roundtrip: %v", err)
	}
	duplicate, err := original.Save(root, "session-1")
	if err != nil || duplicate.Path != saved.Path {
		t.Fatalf("deduplication: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, saved.Path), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := saved.Read(); err == nil {
		t.Fatal("expected checksum failure")
	}
	if _, err := original.Save(root, "session-1"); err == nil {
		t.Fatal("expected corrupt existing file to fail")
	}
}

func TestAttachmentRejectsTraversalAndSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "sessions")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	a := Attachment{DataBase64: base64.StdEncoding.EncodeToString(buf.Bytes())}
	if _, err := a.Save(root, "../escape"); err == nil {
		t.Fatal("accepted traversal")
	}
	if _, err := a.Save(root, "safe"); err == nil {
		t.Fatal("followed symlink outside root")
	}
	for _, path := range []string{"../secret", "/tmp/picture.png", "sessions/../images/x.png"} {
		if ValidPath(path) {
			t.Fatalf("accepted %s", path)
		}
	}
}
