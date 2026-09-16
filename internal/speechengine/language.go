package speechengine

import (
	"fmt"
	"strings"
)

func kokoroLangCode(language string) (string, error) {
	lang := normalizeLanguage(language)
	if lang == "" {
		return "", nil
	}
	switch lang {
	case "en":
		return "a", nil
	case "en-gb", "gb":
		return "b", nil
	case "es":
		return "e", nil
	case "fr":
		return "f", nil
	case "hi":
		return "h", nil
	case "it":
		return "i", nil
	case "pt", "pt-br":
		return "p", nil
	case "ja":
		return "j", nil
	case "zh":
		return "z", nil
	default:
		return "", fmt.Errorf("%w: kokoro does not support %q", ErrUnsupportedLanguage, language)
	}
}

func qwen3TTSLanguage(language string) (string, error) {
	lang := normalizeLanguage(language)
	if lang == "" {
		return "", nil
	}
	switch lang {
	case "en":
		return "English", nil
	case "ru":
		return "Russian", nil
	case "zh":
		return "Chinese", nil
	case "ja":
		return "Japanese", nil
	case "ko":
		return "Korean", nil
	case "de":
		return "German", nil
	case "fr":
		return "French", nil
	case "pt":
		return "Portuguese", nil
	case "es":
		return "Spanish", nil
	case "it":
		return "Italian", nil
	default:
		return "", fmt.Errorf("%w: qwen3_tts does not support %q", ErrUnsupportedLanguage, language)
	}
}

func resolveKokoroLangCode(requested, fallback string) (string, error) {
	mapped, err := kokoroLangCode(requested)
	if err != nil {
		return "", err
	}
	if mapped != "" {
		return mapped, nil
	}
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return defaultKokoroLangCode, nil
	}
	return fallback, nil
}

func resolveQwen3TTSLanguage(requested, fallback string) (string, error) {
	mapped, err := qwen3TTSLanguage(requested)
	if err != nil {
		return "", err
	}
	if mapped != "" {
		return mapped, nil
	}
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return defaultQwen3TTSLang, nil
	}
	return fallback, nil
}
