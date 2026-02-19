package integrationtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/stt/whispercpp"
	"github.com/A2gent/brute/internal/tools"
)

const whisperSTTDefaultExt = ".wav"

type WhisperSTTTool struct {
	workDir string
}

type whisperSTTParams struct {
	AudioPath        string `json:"audio_path,omitempty"`
	AudioBytesBase64 string `json:"audio_bytes_base64,omitempty"`
	AudioBase64      string `json:"audio_base64,omitempty"`
	Language         string `json:"language,omitempty"`
	TranslateToEN    *bool  `json:"translate_to_english,omitempty"`
	OutputPath       string `json:"output_path,omitempty"`
}

func NewWhisperSTTTool(workDir string) *WhisperSTTTool {
	return &WhisperSTTTool{workDir: workDir}
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
	var p whisperSTTParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	audioB64 := strings.TrimSpace(p.AudioBytesBase64)
	if audioB64 == "" {
		audioB64 = strings.TrimSpace(p.AudioBase64)
	}
	audioPathRaw := strings.TrimSpace(p.AudioPath)
	if audioPathRaw == "" && audioB64 == "" {
		return &tools.Result{Success: false, Error: "one of audio_path or audio_bytes_base64 is required"}, nil
	}
	if audioPathRaw != "" && audioB64 != "" {
		return &tools.Result{Success: false, Error: "provide either audio_path or audio_bytes_base64, not both"}, nil
	}

	audioPath := ""
	cleanupPath := ""
	if audioB64 != "" {
		raw, err := base64.StdEncoding.DecodeString(audioB64)
		if err != nil {
			return &tools.Result{Success: false, Error: "audio_bytes_base64 is not valid base64"}, nil
		}
		if len(raw) == 0 {
			return &tools.Result{Success: false, Error: "audio_bytes_base64 payload is empty"}, nil
		}
		tmp, err := os.CreateTemp("", "a2gent-whisper-stt-*"+whisperSTTDefaultExt)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create temporary audio file: %v", err)}, nil
		}
		cleanupPath = tmp.Name()
		audioPath = cleanupPath
		if _, err := tmp.Write(raw); err != nil {
			_ = tmp.Close()
			_ = os.Remove(cleanupPath)
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to write audio payload: %v", err)}, nil
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(cleanupPath)
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to finalize audio payload: %v", err)}, nil
		}
	} else {
		resolved := t.resolvePath(audioPathRaw)
		info, err := os.Stat(resolved)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("audio file not found: %v", err)}, nil
		}
		if info.IsDir() {
			return &tools.Result{Success: false, Error: "audio_path must reference a file, not a directory"}, nil
		}
		audioPath = resolved
	}
	if cleanupPath != "" {
		defer os.Remove(cleanupPath)
	}

	start := time.Now()
	transcript, err := whispercpp.TranscribeWithOptions(ctx, audioPath, strings.TrimSpace(p.Language), p.TranslateToEN)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	durationMs := time.Since(start).Milliseconds()

	outputPath := strings.TrimSpace(p.OutputPath)
	metadata := map[string]interface{}{
		"language":             strings.TrimSpace(p.Language),
		"translate_to_english": p.TranslateToEN,
		"audio_path":           audioPath,
		"duration_ms":          durationMs,
		"transcript":           transcript,
		"transcript_len":       len(transcript),
	}
	outputParts := []string{
		"Transcribed audio with whisper.cpp.",
		"Text: " + transcript,
	}

	if outputPath != "" {
		resolvedOut := t.resolvePath(outputPath)
		if err := os.MkdirAll(filepath.Dir(resolvedOut), 0o755); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create transcript folder: %v", err)}, nil
		}
		if err := os.WriteFile(resolvedOut, []byte(transcript+"\n"), 0o644); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to write transcript file: %v", err)}, nil
		}
		metadata["output_path"] = resolvedOut
		outputParts = append(outputParts, "Saved transcript: "+resolvedOut)
	}

	return &tools.Result{
		Success:  true,
		Output:   strings.Join(outputParts, "\n"),
		Metadata: metadata,
	}, nil
}

func (t *WhisperSTTTool) resolvePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	baseDir := strings.TrimSpace(t.workDir)
	if baseDir == "" {
		baseDir = "."
	}
	return filepath.Clean(filepath.Join(baseDir, path))
}

var _ tools.Tool = (*WhisperSTTTool)(nil)
