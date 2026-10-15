package speechengine

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func synthesizeSilero(ctx context.Context, cfg runtimeConfig, text, language string) ([]byte, error) {
	if strings.TrimSpace(cfg.pythonPath) == "" {
		return nil, fmt.Errorf("%w: Silero TTS requires Python with PyTorch; install the Silero runtime", ErrPythonNotConfigured)
	}
	lang := strings.ToLower(strings.TrimSpace(language))
	lang, _, _ = strings.Cut(lang, "-")
	if lang == "" {
		lang = strings.ToLower(cfg.sileroTTSLanguage)
	}
	if lang == "" {
		lang = defaultSileroTTSLang
	}
	if lang != "ru" {
		return nil, fmt.Errorf("%w: initial Silero TTS setup supports Russian only", ErrUnsupportedLanguage)
	}
	sampleRate, err := strconv.Atoi(cfg.sileroTTSSampleRate)
	if err != nil || (sampleRate != 8000 && sampleRate != 16000 && sampleRate != 24000 && sampleRate != 48000) {
		return nil, fmt.Errorf("invalid Silero sample rate %q", cfg.sileroTTSSampleRate)
	}
	scriptPath, cleanup, err := resolveMLXScriptForRun(cfg)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	outputFile, err := os.CreateTemp("", "aagent-silero-tts-*.wav")
	if err != nil {
		return nil, fmt.Errorf("create Silero output file: %w", err)
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer os.Remove(outputPath)

	// Silero swallows digits and Latin words, so spell them out in Russian first.
	prefetchPronunciations(ctx, text)
	text = normalizeRussianText(text)
	args := []string{
		scriptPath, "silero-tts", "--text", text, "--output", outputPath,
		"--model", cfg.sileroTTSModel, "--voice", cfg.sileroTTSVoice,
		"--language", lang, "--sample-rate", strconv.Itoa(sampleRate),
	}
	if !strings.EqualFold(cfg.sileroTTSPutAccent, "true") {
		args = append(args, "--no-put-accent")
	}
	if !strings.EqualFold(cfg.sileroTTSPutYo, "true") {
		args = append(args, "--no-put-yo")
	}
	if _, err := runExternal(ctx, cfg.pythonPath, args...); err != nil {
		return nil, err
	}
	audio, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read Silero audio: %w", err)
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("Silero synthesis produced empty audio")
	}
	return audio, nil
}
