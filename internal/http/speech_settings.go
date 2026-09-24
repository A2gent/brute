package http

import (
	"os"
	"strings"
)

var speechSettingKeys = []string{
	"AAGENT_STT_ENGINE", "AAGENT_TTS_ENGINE", "AAGENT_SPEECH_PYTHON", "AAGENT_WHISPERKIT_BIN",
	"AAGENT_SPEECH_PARAKEET_MODEL", "AAGENT_SPEECH_MOONSHINE_MODEL", "AAGENT_SPEECH_WHISPERKIT_MODEL",
	"AAGENT_SPEECH_KOKORO_MODEL", "AAGENT_SPEECH_KOKORO_VOICE", "AAGENT_SPEECH_KOKORO_LANG_CODE",
	"AAGENT_SPEECH_QWEN3_TTS_MODEL", "AAGENT_SPEECH_QWEN3_TTS_VOICE", "AAGENT_SPEECH_QWEN3_TTS_LANG_CODE",
	"AAGENT_SPEECH_QWEN3_TTS_STYLE_GENDER", "AAGENT_SPEECH_QWEN3_TTS_STYLE_PITCH",
	"AAGENT_SPEECH_QWEN3_TTS_STYLE_EMOTION", "AAGENT_SPEECH_QWEN3_TTS_STYLE_SPEED",
	"AAGENT_SPEECH_QWEN3_TTS_STYLE_EXTRA",
	"AAGENT_WHISPER_LANGUAGE", "AAGENT_WHISPER_TRANSLATE", "PIPER_MODEL",
}

// Speech runtimes and tools share env-based configuration. Sync only speech keys
// so saving app settings updates live inference without exporting unrelated data.
// Explicit shell/container overrides continue to win over saved settings.
func syncSpeechSettings(previous, next map[string]string) {
	for _, key := range speechSettingKeys {
		current := strings.TrimSpace(os.Getenv(key))
		if current != "" && current != strings.TrimSpace(previous[key]) {
			continue
		}
		value := strings.TrimSpace(next[key])
		if value == "" {
			_ = os.Unsetenv(key)
		} else {
			_ = os.Setenv(key, value)
		}
	}
}
