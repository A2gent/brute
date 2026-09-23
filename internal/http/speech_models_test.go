package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/tools"
)

func TestListSpeechModelsIncludesAvailableEngines(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	clipStore := speechcache.New(0)
	manager := tools.NewManager(".")
	manager.Register(&fakeSpeechTool{name: "piper_tts", store: clipStore})
	manager.Register(&fakeSpeechTool{name: "macos_say_tts", store: clipStore})
	server := &Server{toolManager: manager, speechClips: clipStore}

	req := httptest.NewRequest(http.MethodGet, "/speech/models", nil)
	w := httptest.NewRecorder()
	server.handleListSpeechModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var payload speechModelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	byID := map[string]speechModel{}
	for _, model := range payload.Models {
		byID[model.ID] = model
	}
	if _, ok := byID["auto"]; !ok {
		t.Fatalf("auto model missing: %s", w.Body.String())
	}
	piper, ok := byID["piper_tts:ru_RU-ruslan-medium"]
	if !ok || piper.Engine != "piper_tts" || !piper.Available {
		t.Fatalf("piper Russian model missing or unavailable: %+v", piper)
	}
	if _, ok := byID["edge_tts:en-US-EmmaMultilingualNeural"]; ok {
		t.Fatal("unregistered edge models should be omitted")
	}
}

func TestParseSpeechModel(t *testing.T) {
	engine, voice, err := parseSpeechModel("piper_tts:en_US-ryan-high")
	if err != nil || engine != "piper_tts" || voice != "en_US-ryan-high" {
		t.Fatalf("got %q %q %v", engine, voice, err)
	}
	if _, _, err := parseSpeechModel(""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseSpeechModel("auto"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseSpeechModel("nope"); err == nil {
		t.Fatal("expected invalid model error")
	}
}
