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
	name   string
	store  *speechcache.Store
	calls  int
	params json.RawMessage
}

func (t *fakeSpeechTool) Name() string {
	if t.name == "" {
		return "edge_tts"
	}
	return t.name
}
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
	t.Setenv("AAGENT_TTS_ENGINE", "piper_tts")
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	clipStore := speechcache.New(0)
	manager := tools.NewManager(".")
	fake := &fakeSpeechTool{name: "piper_tts", store: clipStore}
	manager.Register(fake)
	server := NewServer(config.DefaultConfig(), nil, manager, session.NewManager(store), store, clipStore, 0)
	// Override the local default tool after NewServer adds the real built-ins.
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
	if params["model_path"] != "ru_RU-ruslan-medium" {
		t.Fatalf("Russian voice not forwarded: %s", fake.params)
	}

}

func TestCompletionSpeechLanguagePayload(t *testing.T) {
	for _, tc := range []struct{ tool, language, key, want string }{
		{"edge_tts", "ru", "voice", "en-US-EmmaMultilingualNeural"},
		{"macos_say_tts", "ru-RU", "voice", "Milena"},
		{"piper_tts", "ru", "model_path", "ru_RU-ruslan-medium"},
		{"edge_tts", "en", "voice", "en-US-EmmaMultilingualNeural"},
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
			if tc.tool == "piper_tts" && tc.language != "" {
				if got["language"] != strings.ToLower(strings.Split(tc.language, "-")[0]) {
					t.Fatalf("piper language = %v, want language hint", got["language"])
				}
			}
		})
	}
}

func TestCompletionSpeechPayloadUsesRequestedModel(t *testing.T) {
	payload, err := completionSpeechPayload(speechCompletionRequest{
		Text:     "Hello",
		Language: "ru",
		Model:    "piper_tts:en_US-ryan-high",
	}, "piper_tts")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got["model_path"] != "en_US-ryan-high" {
		t.Fatalf("selected piper model not applied: %s", payload)
	}
}

func TestCompletionSpeechPrefersRequestedEngine(t *testing.T) {
	clipStore := speechcache.New(0)
	manager := tools.NewManager(".")
	piper := &fakeSpeechTool{name: "piper_tts", store: clipStore}
	edge := &fakeSpeechTool{name: "edge_tts", store: clipStore}
	manager.Register(edge)
	manager.Register(piper)
	server := &Server{toolManager: manager, speechClips: clipStore}

	req := httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(`{"text":"Hello","language":"en","model":"piper_tts:en_US-ryan-high"}`))
	w := httptest.NewRecorder()
	server.handleCompletionSpeech(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if piper.calls != 1 || edge.calls != 0 {
		t.Fatalf("piper calls=%d edge calls=%d, want piper only", piper.calls, edge.calls)
	}
	var params map[string]interface{}
	if err := json.Unmarshal(piper.params, &params); err != nil {
		t.Fatal(err)
	}
	if params["model_path"] != "en_US-ryan-high" {
		t.Fatalf("unexpected piper params: %s", piper.params)
	}
}

func TestCompletionSpeechAutoPrefersPiperOverMacOS(t *testing.T) {
	clipStore := speechcache.New(0)
	manager := tools.NewManager(".")
	piper := &fakeSpeechTool{name: "piper_tts", store: clipStore}
	macos := &fakeSpeechTool{name: "macos_say_tts", store: clipStore}
	manager.Register(macos)
	manager.Register(piper)
	server := &Server{toolManager: manager, speechClips: clipStore}

	req := httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(`{"text":"Hello","language":"en","model":"auto"}`))
	w := httptest.NewRecorder()
	server.handleCompletionSpeech(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if piper.calls != 1 || macos.calls != 0 {
		t.Fatalf("auto ranking should try piper before macOS, piper=%d macos=%d", piper.calls, macos.calls)
	}
}

func TestCompletionSpeechRejectsUnknownModel(t *testing.T) {
	server := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(`{"text":"Hello","model":"nope"}`))
	w := httptest.NewRecorder()
	server.handleCompletionSpeech(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
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
