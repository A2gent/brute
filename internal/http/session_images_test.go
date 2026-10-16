package http

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestSessionImageResponseAndLazyAsset(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := session.NewManager(store)
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 3)))
	sess := session.New("build")
	sess.AddUserMessageWithImages("screen", []session.ImageAttachment{{DataBase64: base64.StdEncoding.EncodeToString(buf.Bytes()), MediaType: "image/png"}})
	if err := manager.Save(sess); err != nil {
		t.Fatal(err)
	}
	server := &Server{sessionManager: manager}
	payload := sessionImagesToPayload(sess.Messages[0].Images)[0]
	if payload.Path == "" || payload.DataBase64 != "" || payload.Width != 2 || payload.Height != 3 || payload.SHA256 == "" {
		t.Fatalf("not a reference: %+v", payload)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/assets/images?path="+url.QueryEscape(payload.Path), nil)
	response := httptest.NewRecorder()
	server.handleGetImageAsset(response, request)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), buf.Bytes()) || response.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("image response: %d %v", response.Code, response.Header())
	}
}

func TestIncomingImageReferenceCanBeRetriedIntoAnotherSession(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := session.NewManager(store)
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	source := session.New("build")
	source.AddUserMessageWithImages("source", []session.ImageAttachment{{MediaType: "image/png", DataBase64: base64.StdEncoding.EncodeToString(buf.Bytes())}})
	if err := manager.Save(source); err != nil {
		t.Fatal(err)
	}
	payload := sessionImagesToPayload(source.Messages[0].Images)
	normalized, err := normalizeIncomingImages(payload)
	if err != nil {
		t.Fatal(err)
	}
	target := session.New("build")
	target.AddUserMessageWithImages("retry", normalized)
	if err := manager.Save(target); err != nil {
		t.Fatal(err)
	}
	if target.Messages[0].Images[0].Path == source.Messages[0].Images[0].Path {
		t.Fatal("retry depends on source file")
	}
	if err := manager.Delete(source.ID); err != nil {
		t.Fatal(err)
	}
	original, err := target.Messages[0].Images[0].Read()
	if err != nil || !bytes.Equal(original, buf.Bytes()) {
		t.Fatalf("retry lost attachment: %v", err)
	}
	rec := httptest.NewRecorder()
	server := &Server{sessionManager: manager}
	server.handleGetImageAsset(rec, httptest.NewRequest(http.MethodGet, "/assets/images?path="+url.QueryEscape(payload[0].Path), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted image accessible: %d", rec.Code)
	}
}
