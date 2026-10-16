package session

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestManagerImageSaveLoadAndLinkedCopy(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store)
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 3)))
	data := base64.StdEncoding.EncodeToString(buf.Bytes())
	sess := New("build")
	sess.AddUserMessageWithImages("screenshot", []ImageAttachment{{Name: "test.png", MediaType: "image/png", DataBase64: data}})
	if err := manager.Save(sess); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(stored.Messages[0].Metadata)
	if strings.Contains(string(encoded), "data_base64") || !strings.Contains(string(encoded), "sha256") {
		t.Fatalf("inline database payload: %s", encoded)
	}
	loaded, err := manager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	attachment := loaded.Messages[0].Images[0]
	if attachment.DataBase64 != "" || attachment.Width != 4 || attachment.Height != 3 {
		t.Fatalf("not a lazy reference: %+v", attachment)
	}
	resolved, err := attachment.Inline()
	if err != nil || resolved.DataBase64 != data {
		t.Fatalf("load: %v", err)
	}
	child := New("build")
	child.AddUserMessageWithImages("continue", loaded.Messages[0].Images)
	if err := manager.Save(child); err != nil {
		t.Fatal(err)
	}
	if child.Messages[0].Images[0].Path == attachment.Path {
		t.Fatal("child still depends on parent")
	}
	if err := os.Remove(filepath.Join(manager.ImageRoot(), attachment.Path)); err != nil {
		t.Fatal(err)
	}
	if _, err := child.Messages[0].Images[0].Inline(); err != nil {
		t.Fatal(err)
	}
	// Ordinary session loads do not access image files at all.
	if _, err := manager.Get(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := attachment.Inline(); err == nil {
		t.Fatal("expected missing-file error")
	}
}

func TestQueuedImagesAreReferencesAndDeletionCleansDescendants(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store)
	parent := New("build")
	child := New("build")
	child.ParentID = &parent.ID
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	attachment := ImageAttachment{MediaType: "image/png", DataBase64: base64.RawStdEncoding.EncodeToString(buf.Bytes())}
	parent.AddUserMessageWithImages("image", []ImageAttachment{attachment})
	if err := manager.Save(parent); err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(child); err != nil {
		t.Fatal(err)
	}
	queued, err := manager.QueuePendingMessage(child.ID, "queued", []ImageAttachment{attachment})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.ListPendingMessages(child.ID)
	if err != nil || len(raw) != 1 || strings.Contains(raw[0].Images, "data_base64") {
		t.Fatalf("inline pending image: %v %v", raw, err)
	}
	taken, err := manager.TakePendingMessage(child.ID, queued.ID)
	if err != nil || taken.Images[0].Root == "" {
		t.Fatalf("queue roundtrip: %v", err)
	}
	if _, err := taken.Images[0].Inline(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(parent.ID); err != nil {
		t.Fatal(err)
	}
	for _, image := range []ImageAttachment{parent.Messages[0].Images[0], taken.Images[0]} {
		if _, err := os.Stat(filepath.Join(manager.ImageRoot(), image.Path)); !os.IsNotExist(err) {
			t.Fatalf("deleted session image retained: %v", err)
		}
	}
}
