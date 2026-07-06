package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type fakeSpeechTool struct {
	store *speechcache.Store
	calls int
}

func (t *fakeSpeechTool) Name() string                   { return "edge_tts" }
func (t *fakeSpeechTool) Description() string            { return "fake speech tool" }
func (t *fakeSpeechTool) Schema() map[string]interface{} { return map[string]interface{}{} }
func (t *fakeSpeechTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	t.calls += 1
	clipID := t.store.Save("audio/mpeg", []byte("fake-audio"))
	return &tools.Result{
		Success: true,
		Metadata: map[string]interface{}{
			"audio_clip": map[string]interface{}{
				"clip_id":      clipID,
				"content_type": "audio/mpeg",
			},
		},
	}, nil
}

func TestCompletionSpeechUsesBuiltInTTSTool(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	clipStore := speechcache.New(0)
	manager := tools.NewManager(".")
	fake := &fakeSpeechTool{store: clipStore}
	manager.Register(fake)
	server := NewServer(config.DefaultConfig(), nil, manager, session.NewManager(store), store, clipStore, 0)
	// Override the registered edge_tts tool after NewServer adds the real built-ins.
	server.toolManager.Register(fake)

	req := httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(`{"text":"Explain the change"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	server.handleCompletionSpeech(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("expected audio/mpeg content type, got %q", got)
	}
	if got := w.Body.String(); got != "fake-audio" {
		t.Fatalf("expected generated audio body, got %q", got)
	}
	if fake.calls != 1 {
		t.Fatalf("expected fake TTS tool to be called once, got %d", fake.calls)
	}
}
