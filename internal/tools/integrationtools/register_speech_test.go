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

	for _, name := range []string{"stt", "kokoro_tts", "qwen3_tts", "whisper_stt"} {
		if _, ok := manager.Get(name); !ok {
			t.Fatalf("expected %q to be registered", name)
		}
	}
}
