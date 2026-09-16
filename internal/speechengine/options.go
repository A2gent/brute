package speechengine

import "strings"

type TranscribeOptions struct {
	Language           string
	TranslateToEnglish *bool
	Prompt             string
	Profile            string
}

func normalizeLanguage(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "auto", "detect":
		return ""
	default:
		if idx := strings.IndexAny(value, "-_"); idx > 0 {
			value = value[:idx]
		}
		return value
	}
}

func wantsTranslation(opts TranscribeOptions) bool {
	return opts.TranslateToEnglish != nil && *opts.TranslateToEnglish
}

func normalizePrompt(raw string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
}
