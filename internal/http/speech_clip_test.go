package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/speechcache"
	"github.com/go-chi/chi/v5"
)

// WebKit (Tauri desktop) needs Range support to play <audio>.
func TestHandleGetSpeechClipSupportsRange(t *testing.T) {
	store := speechcache.New(0)
	id := store.Save("audio/mpeg", []byte("0123456789"))
	s := &Server{speechClips: store}
	r := chi.NewRouter()
	r.Get("/speech/clips/{clipID}", s.handleGetSpeechClip)

	req := httptest.NewRequest(http.MethodGet, "/speech/clips/"+id, nil)
	req.Header.Set("Range", "bytes=0-1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if rec.Body.String() != "01" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "01")
	}
	if rec.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
}
