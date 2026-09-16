package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/speechengine"
	"github.com/A2gent/brute/internal/tools"
)

type STTTool struct {
	sttToolBase
}

func NewSTTTool(workDir string) *STTTool {
	return &STTTool{sttToolBase: sttToolBase{workDir: workDir}}
}

func (t *STTTool) Name() string {
	return "stt"
}

func (t *STTTool) Description() string {
	return "Transcribe audio to text using a local speech engine (parakeet, moonshine, whisperkit, or whisper_cpp). Accepts an audio file path or base64-encoded audio bytes."
}

func (t *STTTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"engine": map[string]interface{}{
				"type":        "string",
				"description": "Optional STT engine id. Defaults to AAGENT_STT_ENGINE, then parakeet.",
				"enum":        []string{"parakeet", "moonshine", "whisperkit", "whisper_cpp"},
			},
			"audio_path": map[string]interface{}{
				"type":        "string",
				"description": "Path to an audio file. Relative paths resolve from active work directory.",
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
				"description": "Optional language hint (`auto`, `en`, `ru`, etc).",
			},
			"translate_to_english": map[string]interface{}{
				"type":        "boolean",
				"description": "When true, translate transcript to English (whisperkit and whisper_cpp only).",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Optional transcription prompt/context (whisperkit only).",
			},
			"profile": map[string]interface{}{
				"type":        "string",
				"description": "Optional engine profile (for example: meeting for a larger whisperkit model).",
			},
			"output_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional path to save transcript text. Relative paths resolve from active work directory.",
			},
		},
	}
}

func (t *STTTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p sttParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	engine, err := speechengine.ResolveSTTEngine(p.Engine)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	audioPath, cleanup, fail := t.prepareAudio(p)
	if fail != nil {
		return fail, nil
	}
	if cleanup != nil {
		defer cleanup()
	}

	start := time.Now()
	transcript, err := speechengine.TranscribeSelected(ctx, engine, audioPath, speechengine.TranscribeOptions{
		Language:           strings.TrimSpace(p.Language),
		TranslateToEnglish: p.TranslateToEN,
		Prompt:             strings.TrimSpace(p.Prompt),
		Profile:            strings.TrimSpace(p.Profile),
	})
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	return t.buildTranscriptResult(
		transcript,
		p,
		audioPath,
		time.Since(start).Milliseconds(),
		engine,
		fmt.Sprintf("Transcribed audio with %s.", engine),
	), nil
}

var _ tools.Tool = (*STTTool)(nil)
