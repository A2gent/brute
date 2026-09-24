package speechengine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	qwen3TTSModel         string
	qwen3TTSVoice         string
	qwen3TTSLangCode      string
	qwen3TTSStyleGender   string
	qwen3TTSStylePitch    string
	qwen3TTSStyleEmotion  string
	qwen3TTSStyleSpeed    string
	qwen3TTSStyleExtra    string
}

func loadRuntimeConfig(ctx context.Context, profile string) runtimeConfig {
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, pythonProbeTimeout*time.Second)
	defer cancel()
	return runtimeConfig{
		pythonPath:       resolvePythonForPlatform(probeCtx, currentPlatform()).Path,
		mlxScriptPath:    resolveMLXScriptPath(),
		whisperKitBin:    resolveWhisperKitBin(),
		parakeetModel:    envOrDefault("AAGENT_SPEECH_PARAKEET_MODEL", defaultParakeetModel),
		moonshineModel:   envOrDefault("AAGENT_SPEECH_MOONSHINE_MODEL", defaultMoonshineModel),
		whisperKitModel:  resolveWhisperKitModel(profile),
		kokoroModel:      envOrDefault("AAGENT_SPEECH_KOKORO_MODEL", defaultKokoroModel),
		kokoroVoice:      envOrDefault("AAGENT_SPEECH_KOKORO_VOICE", defaultKokoroVoice),
		kokoroLangCode:   envOrDefault("AAGENT_SPEECH_KOKORO_LANG_CODE", defaultKokoroLangCode),
		qwen3TTSModel:        envOrDefault("AAGENT_SPEECH_QWEN3_TTS_MODEL", defaultQwen3TTSModel),
		qwen3TTSVoice:        envOrDefault("AAGENT_SPEECH_QWEN3_TTS_VOICE", defaultQwen3TTSVoice),
		qwen3TTSLangCode:     envOrDefault("AAGENT_SPEECH_QWEN3_TTS_LANG_CODE", defaultQwen3TTSLang),
		qwen3TTSStyleGender:  strings.TrimSpace(os.Getenv("AAGENT_SPEECH_QWEN3_TTS_STYLE_GENDER")),
		qwen3TTSStylePitch:   strings.TrimSpace(os.Getenv("AAGENT_SPEECH_QWEN3_TTS_STYLE_PITCH")),
		qwen3TTSStyleEmotion: strings.TrimSpace(os.Getenv("AAGENT_SPEECH_QWEN3_TTS_STYLE_EMOTION")),
		qwen3TTSStyleSpeed:   strings.TrimSpace(os.Getenv("AAGENT_SPEECH_QWEN3_TTS_STYLE_SPEED")),
		qwen3TTSStyleExtra:   strings.TrimSpace(os.Getenv("AAGENT_SPEECH_QWEN3_TTS_STYLE_EXTRA")),
	}
}

func loadRuntimeConfigModels(profile string) runtimeConfig {
	return runtimeConfig{
		mlxScriptPath:    resolveMLXScriptPath(),
		whisperKitBin:    resolveWhisperKitBin(),
		parakeetModel:    envOrDefault("AAGENT_SPEECH_PARAKEET_MODEL", defaultParakeetModel),
		moonshineModel:   envOrDefault("AAGENT_SPEECH_MOONSHINE_MODEL", defaultMoonshineModel),
		whisperKitModel:  resolveWhisperKitModel(profile),
		kokoroModel:      envOrDefault("AAGENT_SPEECH_KOKORO_MODEL", defaultKokoroModel),
		kokoroVoice:      envOrDefault("AAGENT_SPEECH_KOKORO_VOICE", defaultKokoroVoice),
		kokoroLangCode:   envOrDefault("AAGENT_SPEECH_KOKORO_LANG_CODE", defaultKokoroLangCode),
		qwen3TTSModel:        envOrDefault("AAGENT_SPEECH_QWEN3_TTS_MODEL", defaultQwen3TTSModel),
		qwen3TTSVoice:        envOrDefault("AAGENT_SPEECH_QWEN3_TTS_VOICE", defaultQwen3TTSVoice),
		qwen3TTSLangCode:     envOrDefault("AAGENT_SPEECH_QWEN3_TTS_LANG_CODE", defaultQwen3TTSLang),
		qwen3TTSStyleGender:  strings.TrimSpace(os.Getenv("AAGENT_SPEECH_QWEN3_TTS_STYLE_GENDER")),
		qwen3TTSStylePitch:   strings.TrimSpace(os.Getenv("AAGENT_SPEECH_QWEN3_TTS_STYLE_PITCH")),
		qwen3TTSStyleEmotion: strings.TrimSpace(os.Getenv("AAGENT_SPEECH_QWEN3_TTS_STYLE_EMOTION")),
		qwen3TTSStyleSpeed:   strings.TrimSpace(os.Getenv("AAGENT_SPEECH_QWEN3_TTS_STYLE_SPEED")),
		qwen3TTSStyleExtra:   strings.TrimSpace(os.Getenv("AAGENT_SPEECH_QWEN3_TTS_STYLE_EXTRA")),
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
	return resolveWhisperKitBinForPlatform(currentPlatform())
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
