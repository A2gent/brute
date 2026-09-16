package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/stt/whispercpp"
	"github.com/A2gent/brute/internal/tools"
)

type WhisperSTTTool struct {
	sttToolBase
}

func NewWhisperSTTTool(workDir string) *WhisperSTTTool {
	return &WhisperSTTTool{sttToolBase: sttToolBase{workDir: workDir}}
}

func (t *WhisperSTTTool) Name() string {
	return "whisper_stt"
}

func (t *WhisperSTTTool) Description() string {
	return "Transcribe audio to text using local whisper.cpp. Accepts an audio file path or base64-encoded audio bytes."
}

func (t *WhisperSTTTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"audio_path": map[string]interface{}{
				"type":        "string",
				"description": "Path to an audio file (wav/mp3/m4a/etc supported by local whisper.cpp build). Relative paths resolve from active work directory.",
			},
			"audio_bytes_base64": map[string]interface{}{
				"type":        "string",
				"description": "Base64-encoded audio bytes. Use this when passing in-memory audio payloads instead of a file path.",
			},
			"audio_base64": map[string]interface{}{
				"type":        "string",
				"description": "Alias for audio_bytes_base64.",
			},
			"language": map[string]interface{}{
				"type":        "string",
				"description": "Optional language hint (`auto`, `en`, `ru`, etc). Defaults to AAGENT_WHISPER_LANGUAGE or auto-detect.",
			},
			"translate_to_english": map[string]interface{}{
				"type":        "boolean",
				"description": "Optional override. When true, translate transcript to English. When false, keep original language. Defaults to AAGENT_WHISPER_TRANSLATE (false).",
			},
			"output_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional path to save transcript text. Relative paths resolve from active work directory.",
			},
		},
	}
}

func (t *WhisperSTTTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p sttParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	audioPath, cleanup, fail := t.prepareAudio(p)
	if fail != nil {
		return fail, nil
	}
	if cleanup != nil {
		defer cleanup()
	}

	start := time.Now()
	transcript, err := whispercpp.TranscribeWithOptions(ctx, audioPath, strings.TrimSpace(p.Language), p.TranslateToEN)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	return t.buildTranscriptResult(
		transcript,
		p,
		audioPath,
		time.Since(start).Milliseconds(),
		"whisper_cpp",
		"Transcribed audio with whisper.cpp.",
	), nil
}

var _ tools.Tool = (*WhisperSTTTool)(nil)
