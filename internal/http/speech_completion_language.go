package http

import (
	"encoding/json"
	"strings"
)

var defaultCompletionVoices = map[string]map[string]string{
	"edge_tts":      {"ru": "en-US-EmmaMultilingualNeural", "en": "en-US-EmmaMultilingualNeural"},
	"macos_say_tts": {"ru": "Milena", "en": "Samantha"},
	"piper_tts":     {"ru": "ru_RU-ruslan-medium", "en": "en_US-lessac-medium"},
}

func completionSpeechPayload(request speechCompletionRequest, toolName string) ([]byte, error) {
	payload := map[string]interface{}{
		"text":            request.Text,
		"output_mode":     "stream",
		"auto_play_audio": false,
	}
	engine, selectedVoice, err := parseSpeechModel(request.Model)
	if err != nil {
		return nil, err
	}
	// Explicit language avoids reading Russian articles with an English-only voice.
	// Omitted language preserves existing caller configuration.
	language := strings.ToLower(strings.Split(request.Language, "-")[0])
	voice := ""
	if engine == toolName && selectedVoice != "" {
		voice = selectedVoice
	} else if language == "ru" || language == "en" {
		voice = defaultCompletionVoices[toolName][language]
	}
	if voice != "" {
		switch toolName {
		case "piper_tts":
			payload["model_path"] = voice
		case "elevenlabs_tts":
			payload["model_id"] = voice
		default:
			payload["voice"] = voice
		}
	}
	if (toolName == "piper_tts" || toolName == "kokoro_tts" || toolName == "qwen3_tts" || toolName == "silero_tts") && language != "" {
		payload["language"] = language
	}
	return json.Marshal(payload)
}

// detectSpeechLanguage guesses ru or en from the share of Cyrillic letters, so engines that
// support only one language (Silero: Russian) are not fed text in the other.
func detectSpeechLanguage(text string) string {
	var cyr, lat int
	for _, r := range text {
		switch {
		case r >= 0x0400 && r <= 0x04FF:
			cyr++
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			lat++
		}
	}
	if cyr > lat {
		return "ru"
	}
	return "en"
}
