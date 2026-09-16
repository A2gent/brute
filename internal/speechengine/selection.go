package speechengine

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/A2gent/brute/internal/stt/whispercpp"
)

const EngineWhisperCPP = "whisper_cpp"

func ResolveSTTEngine(requested string) (string, error) {
	engine := normalizeEngineID(requested)
	if engine == "" {
		engine = normalizeEngineID(os.Getenv("AAGENT_STT_ENGINE"))
	}
	if engine == "" {
		engine = EngineParakeet
	}
	switch engine {
	case EngineParakeet, EngineMoonshine, EngineWhisperKit, EngineWhisperCPP, "whispercpp":
		if engine == "whispercpp" {
			return EngineWhisperCPP, nil
		}
		return engine, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownEngine, engine)
	}
}

func TranscribeSelected(ctx context.Context, engine string, audioPath string, opts TranscribeOptions) (string, error) {
	var err error
	engine, err = ResolveSTTEngine(engine)
	if err != nil {
		return "", err
	}
	if engine == EngineWhisperKit {
		if strings.TrimSpace(opts.Language) == "" {
			opts.Language = os.Getenv("AAGENT_WHISPER_LANGUAGE")
		}
		if opts.TranslateToEnglish == nil {
			value := strings.ToLower(os.Getenv("AAGENT_WHISPER_TRANSLATE"))
			translate := value == "true" || value == "1" || value == "yes" || value == "on"
			opts.TranslateToEnglish = &translate
		}
	}
	switch engine {
	case EngineWhisperCPP, "whispercpp":
		return whispercpp.TranscribeWithConfig(ctx, audioPath, whispercpp.TranscribeOptions{Language: opts.Language, TranslateToEnglish: opts.TranslateToEnglish, Prompt: opts.Prompt, Profile: opts.Profile})
	default:
		return Transcribe(ctx, engine, audioPath, opts)
	}
}
