package speechengine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	defaultParakeetModel   = "mlx-community/parakeet-tdt-0.6b-v3"
	defaultMoonshineModel  = "UsefulSensors/moonshine-base"
	defaultWhisperKitModel = "large-v3-turbo"
	defaultKokoroModel     = "mlx-community/Kokoro-82M-bf16"
	defaultKokoroVoice     = "af_heart"
	defaultKokoroLangCode  = "a"
	defaultQwen3TTSModel   = "mlx-community/Qwen3-TTS-12Hz-1.7B-CustomVoice-8bit"
	defaultQwen3TTSVoice   = "Ryan"
	defaultQwen3TTSLang    = "English"
)

type runtimeConfig struct {
	pythonPath       string
	mlxScriptPath    string
	whisperKitBin    string
	parakeetModel    string
	moonshineModel   string
	whisperKitModel  string
	kokoroModel      string
	kokoroVoice      string
	kokoroLangCode   string
	qwen3TTSModel    string
	qwen3TTSVoice    string
	qwen3TTSLangCode string
}

func loadRuntimeConfig(profile string) runtimeConfig {
	return runtimeConfig{
		pythonPath:       resolvePythonPath(),
		mlxScriptPath:    resolveMLXScriptPath(),
		whisperKitBin:    resolveWhisperKitBin(),
		parakeetModel:    envOrDefault("AAGENT_SPEECH_PARAKEET_MODEL", defaultParakeetModel),
		moonshineModel:   envOrDefault("AAGENT_SPEECH_MOONSHINE_MODEL", defaultMoonshineModel),
		whisperKitModel:  resolveWhisperKitModel(profile),
		kokoroModel:      envOrDefault("AAGENT_SPEECH_KOKORO_MODEL", defaultKokoroModel),
		kokoroVoice:      envOrDefault("AAGENT_SPEECH_KOKORO_VOICE", defaultKokoroVoice),
		kokoroLangCode:   envOrDefault("AAGENT_SPEECH_KOKORO_LANG_CODE", defaultKokoroLangCode),
		qwen3TTSModel:    envOrDefault("AAGENT_SPEECH_QWEN3_TTS_MODEL", defaultQwen3TTSModel),
		qwen3TTSVoice:    envOrDefault("AAGENT_SPEECH_QWEN3_TTS_VOICE", defaultQwen3TTSVoice),
		qwen3TTSLangCode: envOrDefault("AAGENT_SPEECH_QWEN3_TTS_LANG_CODE", defaultQwen3TTSLang),
	}
}

func pathExists(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func resolvePythonPath() string {
	if raw := strings.TrimSpace(os.Getenv("AAGENT_SPEECH_PYTHON")); raw != "" {
		path := filepath.Clean(raw)
		if pathExists(path) {
			return path
		}
		return ""
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		for _, candidate := range []string{"python3", "python"} {
			if path, err := lookPath(candidate); err == nil {
				return path
			}
		}
	}
	return ""
}

func resolveMLXScriptPath() string {
	if raw := strings.TrimSpace(os.Getenv("AAGENT_SPEECH_MLX_SCRIPT")); raw != "" {
		path := filepath.Clean(raw)
		if pathExists(path) {
			return path
		}
	}
	return ""
}

func resolveWhisperKitBin() string {
	for _, key := range []string{"AAGENT_SPEECH_WHISPERKIT_BIN", "AAGENT_WHISPERKIT_BIN"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			path := filepath.Clean(raw)
			if pathExists(path) {
				return path
			}
			return ""
		}
	}
	for _, candidate := range []string{"whisperkit-cli", "argmax-cli"} {
		if path, err := lookPath(candidate); err == nil {
			return path
		}
	}
	return ""
}

func resolveWhisperKitModel(profile string) string {
	if raw := strings.TrimSpace(os.Getenv("AAGENT_SPEECH_WHISPERKIT_MODEL")); raw != "" {
		return raw
	}
	if strings.EqualFold(strings.TrimSpace(profile), "meeting") {
		if raw := strings.TrimSpace(os.Getenv("AAGENT_SPEECH_WHISPERKIT_MEETING_MODEL")); raw != "" {
			return raw
		}
		return "large-v3"
	}
	return defaultWhisperKitModel
}

func envOrDefault(key, fallback string) string {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		return raw
	}
	return fallback
}
