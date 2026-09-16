package speechengine

import "errors"

var (
	ErrUnknownEngine            = errors.New("unknown speech engine")
	ErrUnsupportedLanguage      = errors.New("language not supported by engine")
	ErrTranslationNotSupported  = errors.New("translation not supported by engine")
	ErrPythonNotConfigured      = errors.New("python runtime for mlx-audio is not configured")
	ErrMLXScriptNotFound        = errors.New("local speech mlx helper script not found")
	ErrWhisperKitNotConfigured  = errors.New("whisperkit-cli is not configured")
	ErrWhisperKitReportMissing  = errors.New("whisperkit JSON report not found")
	ErrEmptyTranscript          = errors.New("no speech detected")
	ErrEmptyText                = errors.New("text is required")
	ErrInvalidAudioPath         = errors.New("audio path is required")
	ErrInvalidEngineForTranscribe = errors.New("engine does not support transcription")
	ErrInvalidEngineForSynthesize = errors.New("engine does not support synthesis")
)
