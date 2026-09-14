package http

import (
	"encoding/json"
	"strings"
)

func completionSpeechPayload(request speechCompletionRequest, toolName string) ([]byte, error) {
	payload := map[string]interface{}{
		"text":            request.Text,
		"output_mode":     "stream",
		"auto_play_audio": false,
	}
	// Explicit language avoids reading Russian articles with Edge's default
	// English voice. Omitted language preserves existing caller configuration.
	language := strings.ToLower(strings.Split(request.Language, "-")[0])
	if language == "ru" || language == "en" {
		voices := map[string]map[string]string{
			"edge_tts":      {"ru": "ru-RU-SvetlanaNeural", "en": "en-US-AriaNeural"},
			"macos_say_tts": {"ru": "Milena", "en": "Samantha"},
			"piper_tts":     {"ru": "ru_RU-ruslan-medium", "en": "en_US-lessac-medium"},
		}
		if voice := voices[toolName][language]; voice != "" {
			key := "voice"
			if toolName == "piper_tts" {
				key = "model_path"
			}
			payload[key] = voice
		}
	}
	return json.Marshal(payload)
}
