package http

import (
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSpeechSettingsApplyUpdateResetAndRespectEnvironment(t *testing.T) {
	for _, key := range speechSettingKeys {
		t.Setenv(key, "")
	}
	first := map[string]string{"AAGENT_STT_ENGINE": "moonshine", "AAGENT_TTS_ENGINE": "kokoro"}
	syncSpeechSettings(nil, first)
	if os.Getenv("AAGENT_STT_ENGINE") != "moonshine" {
		t.Fatal("initial selection not applied")
	}
	second := map[string]string{"AAGENT_STT_ENGINE": "whisperkit"}
	syncSpeechSettings(first, second)
	if os.Getenv("AAGENT_STT_ENGINE") != "whisperkit" || os.Getenv("AAGENT_TTS_ENGINE") != "" {
		t.Fatal("update/reset not applied")
	}
	t.Setenv("AAGENT_STT_ENGINE", "parakeet")
	syncSpeechSettings(second, first)
	if os.Getenv("AAGENT_STT_ENGINE") != "parakeet" {
		t.Fatal("shell override overwritten")
	}
}

func TestSpeechSettingsPersistedHTTPUpdateAndRestart(t *testing.T) {
	for _, key := range speechSettingKeys {
		t.Setenv(key, "")
	}
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	server := NewServer(cfg, nil, nil, session.NewManager(store), store, nil, 0)
	for _, engine := range []string{"moonshine", "whisperkit"} {
		rec := httptest.NewRecorder()
		server.handleUpdateSettings(rec, httptest.NewRequest("PUT", "/settings", strings.NewReader(`{"settings":{"AAGENT_STT_ENGINE":"`+engine+`","AAGENT_TTS_ENGINE":"kokoro"}}`)))
		if rec.Code != 200 || os.Getenv("AAGENT_STT_ENGINE") != engine {
			t.Fatalf("update: %d %s engine=%s", rec.Code, rec.Body.String(), os.Getenv("AAGENT_STT_ENGINE"))
		}
	}
	_ = os.Unsetenv("AAGENT_STT_ENGINE")
	_ = os.Unsetenv("AAGENT_TTS_ENGINE")
	_ = NewServer(cfg, nil, nil, session.NewManager(store), store, nil, 0)
	if os.Getenv("AAGENT_STT_ENGINE") != "whisperkit" || os.Getenv("AAGENT_TTS_ENGINE") != "kokoro" {
		t.Fatal("persisted engines not restored")
	}
}

func TestSpeechSettingsKeepLegacyCustomEnvOnStartup(t *testing.T) {
	t.Setenv("AAGENT_STT_ENGINE", "moonshine")
	syncSpeechSettings(nil, nil)
	if os.Getenv("AAGENT_STT_ENGINE") != "moonshine" {
		t.Fatal("legacy custom env lost")
	}
}
