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
	store  *speechcache.Store
	calls  int
	params json.RawMessage
}

func (t *fakeSpeechTool) Name() string                   { return "edge_tts" }
func (t *fakeSpeechTool) Description() string            { return "fake speech tool" }
func (t *fakeSpeechTool) Schema() map[string]interface{} { return map[string]interface{}{} }
func (t *fakeSpeechTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	t.calls += 1
	t.params = append(json.RawMessage(nil), params...)
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
	// Exercise the HTTP request wiring, not only the payload helper.
	req = httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(`{"text":"Привет", "language":"ru-RU"}`))
	w = httptest.NewRecorder()
	server.handleCompletionSpeech(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Russian synthesis: %d: %s", w.Code, w.Body.String())
	}
	var params map[string]interface{}
	if err := json.Unmarshal(fake.params, &params); err != nil {
		t.Fatal(err)
	}
	if params["voice"] != "ru-RU-SvetlanaNeural" {
		t.Fatalf("Russian voice not forwarded: %s", fake.params)
	}

}

func TestCompletionSpeechLanguagePayload(t *testing.T) {
	for _, tc := range []struct{ tool, language, key, want string }{
		{"edge_tts", "ru", "voice", "ru-RU-SvetlanaNeural"},
		{"macos_say_tts", "ru-RU", "voice", "Milena"},
		{"piper_tts", "ru", "model_path", "ru_RU-ruslan-medium"},
		{"edge_tts", "en", "voice", "en-US-AriaNeural"},
		{"macos_say_tts", "en", "voice", "Samantha"},
		{"piper_tts", "en", "model_path", "en_US-lessac-medium"},
		{"edge_tts", "", "voice", ""},
	} {
		t.Run(tc.tool+"/"+tc.language, func(t *testing.T) {
			payload, err := completionSpeechPayload(speechCompletionRequest{Text: "Привет", Language: tc.language}, tc.tool)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]interface{}
			if err := json.Unmarshal(payload, &got); err != nil {
				t.Fatal(err)
			}
			if got["text"] != "Привет" || got["auto_play_audio"] != false || got["output_mode"] != "stream" {
				t.Fatalf("unexpected payload: %s", payload)
			}
			value, _ := got[tc.key].(string)
			if value != tc.want {
				t.Fatalf("%s = %q, want %q", tc.key, value, tc.want)
			}
		})
	}
}

func TestCompletionSpeechRejectsUnsupportedLanguage(t *testing.T) {
	server := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(`{"text":"Hello", "language":"unknown"}`))
	w := httptest.NewRecorder()
	server.handleCompletionSpeech(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}
