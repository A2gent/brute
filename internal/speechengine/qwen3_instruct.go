package speechengine

import (
	"strings"
)

// Qwen3 CustomVoice (1.7B) accepts natural-language instruct for tone, pitch, and emotion.
// See https://qwenlm-qwen3-tts.mintlify.app/guides/custom-voice

func qwen3ModelSupportsInstruct(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return true
	}
	return strings.Contains(model, "1.7b")
}

func buildQwen3TTSInstruct(cfg runtimeConfig) string {
	if !qwen3ModelSupportsInstruct(cfg.qwen3TTSModel) {
		return ""
	}
	var parts []string
	if phrase := qwen3StyleGenderPhrase(cfg.qwen3TTSStyleGender); phrase != "" {
		parts = append(parts, phrase)
	}
	if phrase := qwen3StylePitchPhrase(cfg.qwen3TTSStylePitch); phrase != "" {
		parts = append(parts, phrase)
	}
	if phrase := qwen3StyleEmotionPhrase(cfg.qwen3TTSStyleEmotion); phrase != "" {
		parts = append(parts, phrase)
	}
	if phrase := qwen3StyleSpeedPhrase(cfg.qwen3TTSStyleSpeed); phrase != "" {
		parts = append(parts, phrase)
	}
	if extra := strings.TrimSpace(cfg.qwen3TTSStyleExtra); extra != "" {
		parts = append(parts, extra)
	}
	return strings.Join(parts, ", ")
}

func qwen3StyleGenderPhrase(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "male":
		return "male voice"
	case "female":
		return "female voice"
	default:
		return ""
	}
}

func qwen3StylePitchPhrase(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "low":
		return "low pitch"
	case "medium":
		return "medium pitch"
	case "high":
		return "high pitch"
	default:
		return ""
	}
}

func qwen3StyleEmotionPhrase(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "calm":
		return "calm and warm tone"
	case "happy":
		return "happy and friendly tone"
	case "excited":
		return "excited and energetic tone"
	case "sad":
		return "sad and melancholic tone"
	case "angry":
		return "very angry tone"
	case "serious":
		return "serious and authoritative tone"
	case "whisper":
		return "whispering, intimate delivery"
	default:
		return ""
	}
}

func qwen3StyleSpeedPhrase(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "very_slow":
		return "speak very slowly"
	case "slow":
		return "speak slowly and calmly"
	case "fast":
		return "speak quickly"
	case "very_fast":
		return "speak very quickly"
	default:
		return ""
	}
}
