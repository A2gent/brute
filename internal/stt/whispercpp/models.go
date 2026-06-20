package whispercpp

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	defaultModelAlias   = "small"
	defaultMeetingAlias = "large-v3-turbo"
)

var whisperModelAliases = map[string]string{
	"fast":                    "ggml-tiny.bin",
	"fast-english":            "ggml-tiny.en.bin",
	"tiny":                    "ggml-tiny.bin",
	"tiny-en":                 "ggml-tiny.en.bin",
	"tiny-english":            "ggml-tiny.en.bin",
	"nano":                    "ggml-base.bin",
	"nano-english":            "ggml-base.en.bin",
	"base":                    "ggml-base.bin",
	"base-en":                 "ggml-base.en.bin",
	"base-english":            "ggml-base.en.bin",
	"standard":                "ggml-small.bin",
	"standard-english":        "ggml-small.en.bin",
	"small":                   "ggml-small.bin",
	"small-en":                "ggml-small.en.bin",
	"small-english":           "ggml-small.en.bin",
	"pro":                     "ggml-medium.bin",
	"pro-english":             "ggml-medium.en.bin",
	"medium":                  "ggml-medium.bin",
	"medium-en":               "ggml-medium.en.bin",
	"medium-english":          "ggml-medium.en.bin",
	"turbo":                   "ggml-large-v3-turbo.bin",
	"large-v3-turbo":          "ggml-large-v3-turbo.bin",
	"ultra-v3-turbo":          "ggml-large-v3-turbo.bin",
	"large":                   "ggml-large-v3.bin",
	"large-v3":                "ggml-large-v3.bin",
	"large-v2":                "ggml-large-v2.bin",
	"ultra":                   "ggml-large-v3.bin",
	"ggml-tiny.bin":           "ggml-tiny.bin",
	"ggml-tiny.en.bin":        "ggml-tiny.en.bin",
	"ggml-base.bin":           "ggml-base.bin",
	"ggml-base.en.bin":        "ggml-base.en.bin",
	"ggml-small.bin":          "ggml-small.bin",
	"ggml-small.en.bin":       "ggml-small.en.bin",
	"ggml-medium.bin":         "ggml-medium.bin",
	"ggml-medium.en.bin":      "ggml-medium.en.bin",
	"ggml-large-v2.bin":       "ggml-large-v2.bin",
	"ggml-large-v3.bin":       "ggml-large-v3.bin",
	"ggml-large-v3-turbo.bin": "ggml-large-v3-turbo.bin",
}

func resolveModelName(requested string, profile string) (string, error) {
	candidates := []string{requested}
	if isMeetingProfile(profile) {
		candidates = append(candidates, os.Getenv("AAGENT_WHISPER_MEETING_MODEL_NAME"))
	}
	candidates = append(candidates, os.Getenv("AAGENT_WHISPER_MODEL_NAME"))
	if isMeetingProfile(profile) {
		candidates = append(candidates, defaultMeetingAlias)
	} else {
		candidates = append(candidates, defaultModelAlias)
	}

	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		modelName, ok := normalizeModelName(candidate)
		if !ok {
			return "", fmt.Errorf("unknown whisper.cpp model %q; use one of: %s", strings.TrimSpace(candidate), strings.Join(availableModelAliases(), ", "))
		}
		return modelName, nil
	}

	return whisperModelAliases[defaultModelAlias], nil
}

func isMeetingProfile(profile string) bool {
	switch strings.TrimSpace(strings.ToLower(profile)) {
	case "meeting", "meetings", "high", "quality":
		return true
	default:
		return false
	}
}

func normalizeModelName(raw string) (string, bool) {
	key := strings.TrimSpace(strings.ToLower(raw))
	if key == "" {
		return "", false
	}
	key = strings.ReplaceAll(key, "_", "-")
	modelName, ok := whisperModelAliases[key]
	return modelName, ok
}

func availableModelAliases() []string {
	aliases := make([]string, 0, len(whisperModelAliases))
	for alias := range whisperModelAliases {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return aliases
}
