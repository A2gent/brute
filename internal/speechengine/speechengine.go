package speechengine

import (
	"context"
	"fmt"
	"strings"
)

func Transcribe(ctx context.Context, engine string, audioPath string, opts TranscribeOptions) (string, error) {
	engine = normalizeEngineID(engine)
	if engine == "" {
		return "", fmt.Errorf("%w: engine id is required", ErrUnknownEngine)
	}
	if !isTranscribeEngine(engine) {
		return "", fmt.Errorf("%w: %q", ErrInvalidEngineForTranscribe, engine)
	}
	if err := ensureRegularFile(audioPath); err != nil {
		return "", err
	}

	cfg := loadRuntimeConfig(strings.TrimSpace(opts.Profile))
	switch engine {
	case EngineParakeet:
		return transcribeMLX(ctx, cfg, EngineParakeet, audioPath, opts)
	case EngineMoonshine:
		return transcribeMLX(ctx, cfg, EngineMoonshine, audioPath, opts)
	case EngineWhisperKit:
		return transcribeWhisperKit(ctx, cfg, audioPath, opts)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownEngine, engine)
	}
}

func Synthesize(ctx context.Context, engine string, text string) ([]byte, error) {
	return SynthesizeWithLanguage(ctx, engine, text, "")
}

func SynthesizeWithLanguage(ctx context.Context, engine string, text string, language string) ([]byte, error) {
	engine = normalizeEngineID(engine)
	if engine == "" {
		return nil, fmt.Errorf("%w: engine id is required", ErrUnknownEngine)
	}
	if !isSynthesizeEngine(engine) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidEngineForSynthesize, engine)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, ErrEmptyText
	}

	cfg := loadRuntimeConfig("")
	switch engine {
	case EngineKokoro:
		return synthesizeMLX(ctx, cfg, EngineKokoro, text, language)
	case EngineQwen3TTS:
		return synthesizeMLX(ctx, cfg, EngineQwen3TTS, text, language)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownEngine, engine)
	}
}
