package speechengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func transcribeMLX(ctx context.Context, cfg runtimeConfig, engine, audioPath string, opts TranscribeOptions) (string, error) {
	if err := validateMLXTranscribe(engine, opts); err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.pythonPath) == "" {
		return "", fmt.Errorf("%w: set AAGENT_SPEECH_PYTHON or install python3 with mlx-audio (see docs/local-speech.md)", ErrPythonNotConfigured)
	}

	scriptPath, cleanup, err := resolveMLXScriptForRun(cfg)
	if err != nil {
		return "", err
	}
	defer cleanup()

	model := cfg.parakeetModel
	if engine == EngineMoonshine {
		model = cfg.moonshineModel
	}

	args := []string{
		scriptPath,
		"stt",
		"--engine", engine,
		"--audio", audioPath,
		"--model", model,
	}
	// lean: parakeet/moonshine autodetect language; prompt is not supported
	if engine == EngineMoonshine {
		if lang := normalizeLanguage(opts.Language); lang != "" && lang != "en" {
			args = append(args, "--language", lang)
		}
	}

	result, err := runExternal(ctx, cfg.pythonPath, args...)
	if err != nil {
		return "", err
	}
	return parseMLXTranscript(result.stdout)
}

func synthesizeMLX(ctx context.Context, cfg runtimeConfig, engine, text, language string) ([]byte, error) {
	if strings.TrimSpace(cfg.pythonPath) == "" {
		return nil, fmt.Errorf("%w: set AAGENT_SPEECH_PYTHON or install python3 with mlx-audio (see docs/local-speech.md)", ErrPythonNotConfigured)
	}

	scriptPath, cleanup, err := resolveMLXScriptForRun(cfg)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	outputFile, err := os.CreateTemp("", "aagent-speech-tts-*.wav")
	if err != nil {
		return nil, fmt.Errorf("create tts output temp file: %w", err)
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer os.Remove(outputPath)

	args := []string{
		scriptPath,
		"tts",
		"--engine", engine,
		"--text", text,
		"--output", outputPath,
	}
	switch engine {
	case EngineKokoro:
		langCode, err := resolveKokoroLangCode(language, cfg.kokoroLangCode)
		if err != nil {
			return nil, err
		}
		args = append(args,
			"--model", cfg.kokoroModel,
			"--voice", cfg.kokoroVoice,
			"--lang-code", langCode,
		)
	case EngineQwen3TTS:
		qwenLang, err := resolveQwen3TTSLanguage(language, cfg.qwen3TTSLangCode)
		if err != nil {
			return nil, err
		}
		args = append(args,
			"--model", cfg.qwen3TTSModel,
			"--voice", cfg.qwen3TTSVoice,
			"--language", qwenLang,
		)
		if instruct := buildQwen3TTSInstruct(cfg); instruct != "" {
			args = append(args, "--instruct", instruct)
		}
	}

	if _, err := runExternal(ctx, cfg.pythonPath, args...); err != nil {
		return nil, err
	}
	audio, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read synthesized audio: %w", err)
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("synthesis produced empty audio")
	}
	return audio, nil
}

func resolveMLXScriptForRun(cfg runtimeConfig) (path string, cleanup func(), err error) {
	cleanup = func() {}
	if script := strings.TrimSpace(cfg.mlxScriptPath); script != "" {
		return script, cleanup, nil
	}
	return materializeEmbeddedMLXScript()
}

func validateMLXTranscribe(engine string, opts TranscribeOptions) error {
	if wantsTranslation(opts) {
		return fmt.Errorf("%w: %s only supports transcription", ErrTranslationNotSupported, engine)
	}
	lang := normalizeLanguage(opts.Language)
	if engine == EngineMoonshine && lang != "" && lang != "en" {
		return fmt.Errorf("%w: moonshine supports English only", ErrUnsupportedLanguage)
	}
	return nil
}

func parseMLXTranscript(stdout []byte) (string, error) {
	line := strings.TrimSpace(string(stdout))
	if line == "" {
		return "", ErrEmptyTranscript
	}
	var payload struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return "", fmt.Errorf("invalid mlx stt response: %w", err)
	}
	if msg := strings.TrimSpace(payload.Error); msg != "" {
		return "", fmt.Errorf("mlx stt failed: %s", msg)
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		return "", ErrEmptyTranscript
	}
	return text, nil
}
