package integrationtools

import (
	"testing"

	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestRegisterIncludesLocalSpeechTools(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	manager := tools.NewManager(".")
	Register(manager, store, speechcache.New(0), nil)

	for _, name := range []string{"stt"} {
		if _, ok := manager.Get(name); !ok {
			t.Fatalf("expected %q to be registered", name)
		}
	}
}

func TestEngineSpeechToolsNotRegisteredWithManager(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	manager := tools.NewManager(".")
	clips := speechcache.New(0)
	Register(manager, store, clips, nil)

	engines := SpeechEngineTools(".", store, clips)
	for _, name := range []string{"edge_tts", "elevenlabs_tts", "macos_say_tts", "piper_tts", "kokoro_tts", "qwen3_tts", "silero_tts"} {
		if _, ok := manager.Get(name); ok {
			t.Errorf("%q must not be a model-facing tool", name)
		}
		if _, ok := engines[name]; !ok {
			t.Errorf("%q missing from SpeechEngineTools", name)
		}
	}
	if _, ok := manager.Get("whisper_stt"); ok {
		t.Error("whisper_stt is covered by stt and should not be registered")
	}
}
