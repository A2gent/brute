package speechengine

import "strings"

const (
	EngineParakeet   = "parakeet"
	EngineMoonshine  = "moonshine"
	EngineWhisperKit = "whisperkit"
	EngineKokoro     = "kokoro"
	EngineQwen3TTS   = "qwen3_tts"
	EngineSileroTTS  = "silero_tts"
)

func normalizeEngineID(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func isTranscribeEngine(engine string) bool {
	switch normalizeEngineID(engine) {
	case EngineParakeet, EngineMoonshine, EngineWhisperKit:
		return true
	default:
		return false
	}
}

func isSynthesizeEngine(engine string) bool {
	switch normalizeEngineID(engine) {
	case EngineKokoro, EngineQwen3TTS, EngineSileroTTS:
		return true
	default:
		return false
	}
}
